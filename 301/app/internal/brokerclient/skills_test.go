// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package brokerclient_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/agentruntime"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/brokerclient"
	"google.golang.org/adk/v2/tool/skilltoolset/skill"
)

type testSkillZipEntry struct {
	name string
	body []byte
	mode os.FileMode
}

func buildTestZip(t *testing.T, entries []testSkillZipEntry) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{
			Name:   e.name,
			Method: zip.Store,
		}
		if e.mode != 0 {
			hdr.SetMode(e.mode)
		} else {
			hdr.SetMode(0o644)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("CreateHeader(%s): %v", e.name, err)
		}
		if _, err := w.Write(e.body); err != nil {
			t.Fatalf("Write(%s): %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close zip: %v", err)
	}
	raw := buf.Bytes()
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:])
}

func TestRegistrySkillSourceDynamicRevisionsAndWorkspaceExecution(t *testing.T) {
	v1SkillMD := []byte("---\nname: ledger-pool-triage\ndescription: Baseline v1 pool check.\nmetadata:\n  revision: v1\n---\n# Runbook v1\nRun scripts/diagnose_pool.sh.\n")
	v1Script := []byte("#!/bin/sh\necho SKILL_REVISION=v1\n")
	v1Zip, v1SHA := buildTestZip(t, []testSkillZipEntry{
		{name: "SKILL.md", body: v1SkillMD, mode: 0o644},
		{name: "scripts/diagnose_pool.sh", body: v1Script, mode: 0o755},
		{name: "references/notes.md", body: []byte("ref-v1"), mode: 0o644},
	})

	v2SkillMD := []byte("---\nname: ledger-pool-triage\ndescription: Enhanced v2 pool and cursor leak check.\nmetadata:\n  revision: v2\n---\n# Runbook v2\nRun scripts/diagnose_pool.sh for cursor leaks.\n")
	v2Script := []byte("#!/bin/sh\necho SKILL_REVISION=v2\n")
	v2Zip, v2SHA := buildTestZip(t, []testSkillZipEntry{
		{name: "SKILL.md", body: v2SkillMD, mode: 0o644},
		{name: "scripts/diagnose_pool.sh", body: v2Script, mode: 0o755},
		{name: "references/notes.md", body: []byte("ref-v2"), mode: 0o644},
	})

	var mu sync.Mutex
	activeRev := "v1"
	archives := map[string][]byte{"v1": v1Zip, "v2": v2Zip}
	shas := map[string]string{"v1": v1SHA, "v2": v2SHA}
	descs := map[string]string{
		"v1": "Baseline v1 pool check.",
		"v2": "Enhanced v2 pool and cursor leak check.",
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		rev := activeRev
		sha := shas[rev]
		desc := descs[rev]
		mu.Unlock()

		switch r.URL.Path {
		case "/agentregistry/skills":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"skills": []map[string]any{
					{
						"name":             "ledger-pool-triage",
						"default_revision": rev,
						"sha256":           sha,
						"frontmatter": map[string]any{
							"name":        "ledger-pool-triage",
							"description": desc,
						},
					},
				},
			})
		case "/agentregistry/skills/ledger-pool-triage":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":             "ledger-pool-triage",
				"default_revision": rev,
				"sha256":           sha,
				"frontmatter": map[string]any{
					"name":        "ledger-pool-triage",
					"description": desc,
				},
			})
		case "/agentregistry/skills/ledger-pool-triage/revisions/v1", "/agentregistry/skills/ledger-pool-triage/revisions/v2":
			wantRev := filepath.Base(r.URL.Path)
			mu.Lock()
			raw := archives[wantRev]
			mu.Unlock()
			if r.URL.Query().Get("alt") == "media" {
				w.Header().Set("Content-Type", "application/zip")
				_, _ = w.Write(raw)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"revision": wantRev})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	wsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, "ledger-api"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := agentruntime.NewWorkspace(wsDir, "")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	src, err := brokerclient.NewSkillSource(ts.URL, wsDir, ts.Client())
	if err != nil {
		t.Fatalf("NewSkillSource: %v", err)
	}

	ctx := context.Background()
	fms, err := src.ListFrontmatters(ctx)
	if err != nil {
		t.Fatalf("ListFrontmatters(v1): %v", err)
	}
	if len(fms) != 1 || fms[0].Name != "ledger-pool-triage" || fms[0].Metadata["revision"] != "v1" {
		t.Fatalf("ListFrontmatters(v1) = %+v, want ledger-pool-triage v1", fms)
	}

	instV1, err := src.LoadInstructions(ctx, "ledger-pool-triage")
	if err != nil || !strings.Contains(instV1, "Runbook v1") {
		t.Fatalf("LoadInstructions(v1) = %q, err=%v", instV1, err)
	}

	resources, err := src.ListResources(ctx, "ledger-pool-triage", "")
	if err != nil || len(resources) != 2 {
		t.Fatalf("ListResources(v1) = %v, err=%v; want 2 entries", resources, err)
	}

	rc, err := src.LoadResource(ctx, "ledger-pool-triage", "references/notes.md")
	if err != nil {
		t.Fatalf("LoadResource(v1): %v", err)
	}
	refBytes, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(refBytes) != "ref-v1" {
		t.Errorf("LoadResource(v1) = %q, want ref-v1", string(refBytes))
	}

	// Execute unpacked script via Workspace.RunCommand using both active and versioned paths!
	for _, scriptRel := range []string{
		".skills/ledger-pool-triage/scripts/diagnose_pool.sh",
		".skills/ledger-pool-triage@v1/scripts/diagnose_pool.sh",
	} {
		cmdRes, err := ws.RunCommand(ctx, agentruntime.RunCommandArgs{
			Command: "sh",
			Args:    []string{scriptRel},
			Dir:     "ledger-api",
		})
		if err != nil || !cmdRes.Allowed || cmdRes.ExitCode != 0 || !strings.Contains(cmdRes.Output, "SKILL_REVISION=v1") {
			t.Fatalf("RunCommand(%s) = %+v, err=%v; want SKILL_REVISION=v1", scriptRel, cmdRes, err)
		}
	}

	// Flip defaultRevision to v2 in the broker without recreating RegistrySkillSource or Workspace!
	mu.Lock()
	activeRev = "v2"
	mu.Unlock()

	instV2, err := src.LoadInstructions(ctx, "ledger-pool-triage")
	if err != nil || !strings.Contains(instV2, "Runbook v2") {
		t.Fatalf("LoadInstructions(v2) = %q, err=%v; want Runbook v2", instV2, err)
	}

	cmdResV2, err := ws.RunCommand(ctx, agentruntime.RunCommandArgs{
		Command: "sh",
		Args:    []string{".skills/ledger-pool-triage/scripts/diagnose_pool.sh"},
		Dir:     "ledger-api",
	})
	if err != nil || !cmdResV2.Allowed || cmdResV2.ExitCode != 0 || !strings.Contains(cmdResV2.Output, "SKILL_REVISION=v2") {
		t.Fatalf("RunCommand(v2 active) = %+v, err=%v; want SKILL_REVISION=v2", cmdResV2, err)
	}

	// Verify upstream ADK Go v2 sentinel errors!
	if _, err := src.LoadFrontmatter(ctx, "../Invalid_Name"); !errors.Is(err, skill.ErrInvalidSkillName) {
		t.Errorf("LoadFrontmatter(invalid name) err = %v, want skill.ErrInvalidSkillName", err)
	}
	if _, err := src.LoadFrontmatter(ctx, "nonexistent-skill"); !errors.Is(err, skill.ErrSkillNotFound) {
		t.Errorf("LoadFrontmatter(missing) err = %v, want skill.ErrSkillNotFound", err)
	}
	if _, err := src.LoadResource(ctx, "ledger-pool-triage", "../SKILL.md"); !errors.Is(err, skill.ErrInvalidResourcePath) {
		t.Errorf("LoadResource(traversal) err = %v, want skill.ErrInvalidResourcePath", err)
	}
	if _, err := src.LoadResource(ctx, "ledger-pool-triage", "SKILL.md"); !errors.Is(err, skill.ErrInvalidResourcePath) {
		t.Errorf("LoadResource(root SKILL.md) err = %v, want skill.ErrInvalidResourcePath", err)
	}
	if _, err := src.LoadResource(ctx, "ledger-pool-triage", "scripts/missing.sh"); !errors.Is(err, skill.ErrResourceNotFound) {
		t.Errorf("LoadResource(missing script) err = %v, want skill.ErrResourceNotFound", err)
	}
}

func TestRegistrySkillSourceRejectsTamperedSHA256AndZipSlip(t *testing.T) {
	validSkillMD := []byte("---\nname: ledger-pool-triage\ndescription: Test skill.\n---\n# Test\n")
	goodZip, _ := buildTestZip(t, []testSkillZipEntry{
		{name: "SKILL.md", body: validSkillMD, mode: 0o644},
		{name: "scripts/diagnose_pool.sh", body: []byte("#!/bin/sh\necho ok\n"), mode: 0o755},
	})
	zipSlipArchive, zipSlipSHA := buildTestZip(t, []testSkillZipEntry{
		{name: "SKILL.md", body: validSkillMD, mode: 0o644},
		{name: "../evil.sh", body: []byte("#!/bin/sh\necho pwned\n"), mode: 0o755},
	})
	symlinkArchive, symlinkSHA := buildTestZip(t, []testSkillZipEntry{
		{name: "SKILL.md", body: validSkillMD, mode: 0o644},
		{name: "scripts/link.sh", body: []byte("/etc/passwd"), mode: os.ModeSymlink | 0o777},
	})

	cases := []struct {
		name       string
		servedZip  []byte
		servedHash string
	}{
		{
			name:       "tampered sha256 mismatch",
			servedZip:  goodZip,
			servedHash: strings.Repeat("0", 64),
		},
		{
			name:       "zip-slip parent traversal ../evil.sh",
			servedZip:  zipSlipArchive,
			servedHash: zipSlipSHA,
		},
		{
			name:       "symlink entry inside zip",
			servedZip:  symlinkArchive,
			servedHash: symlinkSHA,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/agentregistry/skills/ledger-pool-triage":
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{
						"name":             "ledger-pool-triage",
						"default_revision": "v1",
						"sha256":           tc.servedHash,
					})
				case "/agentregistry/skills/ledger-pool-triage/revisions/v1":
					w.Header().Set("Content-Type", "application/zip")
					_, _ = w.Write(tc.servedZip)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(ts.Close)

			wsDir := t.TempDir()
			src, err := brokerclient.NewSkillSource(ts.URL, wsDir, ts.Client())
			if err != nil {
				t.Fatalf("NewSkillSource: %v", err)
			}
			if _, err := src.LoadInstructions(context.Background(), "ledger-pool-triage"); err == nil {
				t.Fatalf("LoadInstructions succeeded on %s; want fail-closed error", tc.name)
			}
			if _, statErr := os.Stat(filepath.Join(wsDir, "evil.sh")); !os.IsNotExist(statErr) {
				t.Fatalf("evil.sh escaped into workspace root: %v", statErr)
			}
			if _, statErr := os.Stat(filepath.Join(wsDir, ".skills", "ledger-pool-triage@v1")); !os.IsNotExist(statErr) {
				t.Fatalf("unverified directory .skills/ledger-pool-triage@v1 persisted on disk after rejected archive")
			}
		})
	}
}
