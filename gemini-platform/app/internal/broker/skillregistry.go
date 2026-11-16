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

package broker

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/httptransport"
	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/brokerclient"
	"google.golang.org/adk/v2/tool/skilltoolset/skill"
)

const (
	maxCatalogUploadBodyBytes = 14 << 20 // 14 MiB JSON body ceiling for base64-encoded <=10 MiB zip.
	defaultSkillName          = "ledger-pool-triage"
)

var (
	skillIDPattern    = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	revisionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	sha256HexPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// SkillFrontmatterDTO serializes SKILL.md YAML frontmatter in JSON responses.
type SkillFrontmatterDTO struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	AllowedTools  []string          `json:"allowed_tools,omitempty"`
}

// CatalogSkillRevision describes one immutable Procedural Skill revision in Skill Registry.
type CatalogSkillRevision struct {
	Revision            string              `json:"revision"`
	ResourceName        string              `json:"name"`
	SHA256              string              `json:"sha256"`
	SHA256Hash          string              `json:"sha256Hash"`
	SizeBytes           int64               `json:"size_bytes"`
	State               string              `json:"state"`
	Active              bool                `json:"active"`
	Files               []string            `json:"files"`
	Frontmatter         SkillFrontmatterDTO `json:"frontmatter"`
	InstructionsPreview string              `json:"instructions_preview"`
	CreatedAt           string              `json:"created_at"`
}

// CatalogSkillSummary summarizes a governed Procedural Skill and all its revisions.
type CatalogSkillSummary struct {
	Name            string                 `json:"name"`
	DisplayName     string                 `json:"display_name"`
	Description     string                 `json:"description"`
	ResourceName    string                 `json:"resource_name"`
	DefaultRevision string                 `json:"default_revision"`
	DefaultRevURI   string                 `json:"defaultRevision"`
	ActiveSHA256    string                 `json:"active_sha256"`
	SHA256          string                 `json:"sha256"`
	ActiveSizeBytes int64                  `json:"active_size_bytes"`
	State           string                 `json:"state"`
	Frontmatter     SkillFrontmatterDTO    `json:"frontmatter"`
	UpdatedAt       string                 `json:"updated_at"`
	Revisions       []CatalogSkillRevision `json:"revisions"`
}

// CatalogCapabilityEntry describes one row in the unified capability table on /catalog.
type CatalogCapabilityEntry struct {
	Capability string `json:"capability"`
	Kind       string `json:"kind"`
	Boundary   string `json:"boundary"`
	Stage      string `json:"stage"`
}

// SkillArchiveEntry represents one regular file inside a deterministic skill zip archive.
type SkillArchiveEntry struct {
	RelPath string
	Data    []byte
}

type storedSkillRevision struct {
	revision     string
	resourceName string
	sha256       string
	zipBytes     []byte
	files        []string
	frontmatter  SkillFrontmatterDTO
	instructions string
	createdAt    time.Time
}

type storedSkillRecord struct {
	name            string
	displayName     string
	description     string
	defaultRevision string
	updatedAt       time.Time
	revisionOrder   []string
	revisions       map[string]*storedSkillRevision
}

// skillStatePersister persists broker catalog mutations to disk.
type skillStatePersister interface {
	savePersistedState()
}

type skillRegistryProxy struct {
	mockLLM      bool
	projectID    string
	location     string
	upstreamBase string
	authedClient *http.Client
	recordPolicy func(PolicyEvent)
	persister    skillStatePersister

	mu         sync.RWMutex
	skillOrder []string
	skills     map[string]*storedSkillRecord
}

func newSkillRegistryProxy(ctx context.Context, cfg Config, persister skillStatePersister, record func(PolicyEvent)) (*skillRegistryProxy, error) {
	loc := firstNonEmpty(cfg.SkillRegistryLocation, "global")
	proj := firstNonEmpty(cfg.ProjectID, "local-dev")
	p := &skillRegistryProxy{
		mockLLM:      cfg.MockLLM,
		projectID:    proj,
		location:     loc,
		upstreamBase: strings.TrimRight(strings.TrimSpace(cfg.SkillRegistryUpstreamURL), "/"),
		recordPolicy: record,
		persister:    persister,
		skills:       make(map[string]*storedSkillRecord),
	}

	if err := p.seedBaselineSkill(); err != nil {
		return nil, fmt.Errorf("seed baseline procedural skill: %w", err)
	}

	if !p.mockLLM {
		if p.upstreamBase != "" {
			p.authedClient = &http.Client{Timeout: 30 * time.Second}
		} else if strings.TrimSpace(cfg.ProjectID) != "" {
			if creds, err := credentials.DetectDefault(&credentials.DetectOptions{
				Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"},
			}); err == nil {
				if hc, err := httptransport.NewClient(&httptransport.Options{
					Credentials: creds,
				}); err == nil {
					hc.Timeout = 30 * time.Second
					p.authedClient = hc
				}
			}
		}
	}
	_ = ctx
	return p, nil
}

func (p *skillRegistryProxy) notifyMutated() {
	if p.persister != nil {
		p.persister.savePersistedState()
	}
}

func (p *skillRegistryProxy) cloudEnabled() bool {
	return !p.mockLLM && p.authedClient != nil && p.projectID != "" && p.projectID != "local-dev"
}

func (p *skillRegistryProxy) backendLabel() string {
	if p.cloudEnabled() {
		return "agent-registry-cloud"
	}
	return "broker-skill-registry"
}

func (p *skillRegistryProxy) cloudBaseURL() string {
	if p.upstreamBase != "" {
		return p.upstreamBase
	}
	return "https://agentregistry.googleapis.com"
}

func (p *skillRegistryProxy) skillResourceName(skillName string) string {
	return fmt.Sprintf("projects/%s/locations/%s/skills/%s", p.projectID, p.location, skillName)
}

func (p *skillRegistryProxy) revisionResourceName(skillName, rev string) string {
	return fmt.Sprintf("projects/%s/locations/%s/skills/%s/revisions/%s", p.projectID, p.location, skillName, rev)
}

func (p *skillRegistryProxy) seedBaselineSkill() error {
	v1Zip, v1SHA, err := BuildDeterministicSkillZip([]SkillArchiveEntry{
		{RelPath: "SKILL.md", Data: []byte(seededLedgerPoolTriageV1SkillMD)},
		{RelPath: "scripts/diagnose_pool.sh", Data: []byte(seededLedgerPoolTriageV1Script)},
	})
	if err != nil {
		return err
	}
	_, err = p.upsertRevision(
		defaultSkillName,
		"v1",
		v1SHA,
		v1Zip,
		true,
		p.revisionResourceName(defaultSkillName, "v1"),
		false,
	)
	return err
}

// BuildDeterministicSkillZipFromDir reads all regular non-hidden files under srcDir
// and packages them into a deterministic ZIP_STORED archive identical to
// scripts/publish-skill.sh --build-only.
func BuildDeterministicSkillZipFromDir(srcDir string) ([]byte, string, error) {
	absRoot, err := filepath.Abs(filepath.Clean(srcDir))
	if err != nil {
		return nil, "", err
	}
	var entries []SkillArchiveEntry
	err = filepath.WalkDir(absRoot, func(full string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if full == absRoot {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink %q not permitted in skill directory", full)
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(absRoot, full)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		entries = append(entries, SkillArchiveEntry{
			RelPath: filepath.ToSlash(rel),
			Data:    raw,
		})
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return BuildDeterministicSkillZip(entries)
}

// BuildDeterministicSkillZip constructs a reproducible ZIP_STORED archive with
// sorted POSIX paths, fixed 1980-01-01 00:00:00 DOS timestamps, no extra fields
// or trailing data descriptors, and Unix permissions (0755 on scripts/*.sh, 0644
// elsewhere), matching Python zipfile.ZIP_STORED byte-for-byte.
func BuildDeterministicSkillZip(entries []SkillArchiveEntry) ([]byte, string, error) {
	sorted := make([]SkillArchiveEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].RelPath < sorted[j].RelPath
	})

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range sorted {
		cleanRel := path.Clean(e.RelPath)
		mode := uint32(0o100644)
		if strings.HasPrefix(cleanRel, "scripts/") && strings.HasSuffix(cleanRel, ".sh") {
			mode = 0o100755
		}
		hdr := &zip.FileHeader{
			Name:               cleanRel,
			CreatorVersion:     (3 << 8) | 20, // Unix (3), PKZIP 2.0 (20)
			ReaderVersion:      20,
			Flags:              0,
			Method:             zip.Store,
			ModifiedTime:       0,            // 00:00:00
			ModifiedDate:       (1 << 5) | 1, // 1980-01-01
			CRC32:              crc32.ChecksumIEEE(e.Data),
			CompressedSize64:   uint64(len(e.Data)),
			UncompressedSize64: uint64(len(e.Data)),
			ExternalAttrs:      mode << 16,
		}
		w, err := zw.CreateRaw(hdr)
		if err != nil {
			return nil, "", err
		}
		if _, err := w.Write(e.Data); err != nil {
			return nil, "", err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}
	out := buf.Bytes()
	sum := sha256.Sum256(out)
	return out, hex.EncodeToString(sum[:]), nil
}

// ServeHTTP handles keyless /agentregistry/skills* requests from Incident Actors.
func (p *skillRegistryProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	sub := strings.TrimPrefix(r.URL.Path, "/agentregistry")
	sub = strings.Trim(sub, "/")

	// Also accept full v1 resource paths: v1/projects/{p}/locations/{l}/skills...
	if idx := strings.Index(sub, "skills"); idx > 0 && strings.HasPrefix(sub, "v1/projects/") {
		sub = sub[idx:]
	}

	if sub == "skills" {
		p.syncFromCloudList(r.Context())
		skills := p.listSummaries()
		writeBrokerJSON(w, http.StatusOK, map[string]any{
			"backend": p.backendLabel(),
			"skills":  skills,
		})
		return
	}

	if !strings.HasPrefix(sub, "skills/") {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(sub, "skills/")
	parts := strings.Split(rest, "/")

	switch len(parts) {
	case 1:
		skillName := parts[0]
		if !skillIDPattern.MatchString(skillName) {
			writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid skill name"})
			return
		}
		p.syncFromCloudSkill(r.Context(), skillName)
		summary, ok := p.getSummary(skillName)
		if !ok {
			writeBrokerJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("skill %q not found", skillName)})
			return
		}
		p.emitPolicy("Skill Registry (/agentregistry)", "GetSkill ("+skillName+")", "ALLOW",
			fmt.Sprintf("Resolved defaultRevision=%s (sha256=%s...)", summary.DefaultRevision, shortSHA(summary.ActiveSHA256)))
		writeBrokerJSON(w, http.StatusOK, summary)

	case 2:
		skillName, action := parts[0], parts[1]
		if action != "revisions" || !skillIDPattern.MatchString(skillName) {
			http.NotFound(w, r)
			return
		}
		p.syncFromCloudSkill(r.Context(), skillName)
		summary, ok := p.getSummary(skillName)
		if !ok {
			writeBrokerJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("skill %q not found", skillName)})
			return
		}
		writeBrokerJSON(w, http.StatusOK, map[string]any{
			"skillRevisions": summary.Revisions,
		})

	case 3, 4:
		skillName, lit, revSegment := parts[0], parts[1], parts[2]
		if lit != "revisions" || !skillIDPattern.MatchString(skillName) {
			http.NotFound(w, r)
			return
		}
		wantDownload := strings.EqualFold(r.URL.Query().Get("alt"), "media")
		if len(parts) == 4 {
			if parts[3] != "archive.zip" {
				http.NotFound(w, r)
				return
			}
			wantDownload = true
		}
		if strings.HasSuffix(revSegment, ":download") {
			revSegment = strings.TrimSuffix(revSegment, ":download")
			wantDownload = true
		} else if strings.HasSuffix(revSegment, ".zip") {
			revSegment = strings.TrimSuffix(revSegment, ".zip")
			wantDownload = true
		}
		if !revisionIDPattern.MatchString(revSegment) || strings.Contains(revSegment, "..") {
			writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid revision identifier"})
			return
		}

		p.syncFromCloudRevision(r.Context(), skillName, revSegment)
		revRec, revDTO, ok := p.getRevision(skillName, revSegment)
		if !ok {
			writeBrokerJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("skill revision %s@%s not found", skillName, revSegment)})
			return
		}
		if !wantDownload {
			writeBrokerJSON(w, http.StatusOK, revDTO)
			return
		}

		p.emitPolicy("Skill Registry (/agentregistry)", fmt.Sprintf("DownloadRevision (%s@%s)", skillName, revSegment), "ALLOW",
			fmt.Sprintf("Served verified zip (%d bytes, sha256=%s...) across keyless Actor Boundary", len(revRec.zipBytes), shortSHA(revRec.sha256)))

		h := w.Header()
		h.Set("Content-Type", "application/zip")
		h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", skillName+"-"+revSegment+".zip"))
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Skill-Revision", revSegment)
		h.Set("X-Skill-SHA256", revRec.sha256)
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(revRec.zipBytes)
		}

	default:
		http.NotFound(w, r)
	}
}

// handleCatalog serves GET /api/catalog for the Incident Console /catalog view.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	proj := firstNonEmpty(s.cfg.ProjectID, "local-dev")
	reg := firstNonEmpty(s.cfg.Region, "us-central1")
	baseURL := s.resolveBaseURL(r)

	s.skillRegistry.syncFromCloudList(r.Context())
	skills := s.skillRegistry.listSummaries()
	agents := s.agentRegistry.listAgents(r.Context(), proj, reg, baseURL)
	mcpServers := s.agentRegistry.listMCPServers(r.Context(), proj, reg, baseURL)
	writeBrokerJSON(w, http.StatusOK, map[string]any{
		"backend":      s.skillRegistry.backendLabel(),
		"project_id":   proj,
		"region":       reg,
		"agent_card":   s.buildAgentCard(baseURL),
		"agents":       agents,
		"mcp_servers":  mcpServers,
		"skills":       skills,
		"capabilities": buildCatalogCapabilities(skills),
	})
}

// handleCatalogSkills serves GET and POST /api/catalog/skills.
func (s *Server) handleCatalogSkills(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		s.skillRegistry.syncFromCloudList(r.Context())
		writeBrokerJSON(w, http.StatusOK, map[string]any{
			"backend": s.skillRegistry.backendLabel(),
			"skills":  s.skillRegistry.listSummaries(),
		})

	case http.MethodPost:
		if fetchSite := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); fetchSite != "" &&
			fetchSite != "same-origin" && fetchSite != "none" {
			writeBrokerJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site catalog mutation denied"})
			return
		}
		if ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type"))); !strings.HasPrefix(ct, "application/json") {
			writeBrokerJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type application/json is required"})
			return
		}
		if !s.hasValidAdminToken(r) {
			writeBrokerJSON(w, http.StatusForbidden, map[string]string{"error": "missing or invalid X-Broker-Admin-Token"})
			return
		}

		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCatalogUploadBodyBytes))
		if err != nil {
			writeBrokerJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "skill payload exceeds size limit"})
			return
		}
		var req struct {
			Skill           string `json:"skill"`
			Revision        string `json:"revision"`
			DefaultRevision string `json:"default_revision"`
			SHA256          string `json:"sha256"`
			ArchiveBase64   string `json:"archive_base64"`
			SetDefault      *bool  `json:"set_default"`
			CloudResource   string `json:"cloud_resource"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid skill catalog JSON"})
			return
		}

		skillName := strings.TrimSpace(req.Skill)
		if !skillIDPattern.MatchString(skillName) {
			writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid skill name %q", skillName)})
			return
		}

		// Mode A: Promote an existing immutable revision to defaultRevision.
		if strings.TrimSpace(req.ArchiveBase64) == "" && strings.TrimSpace(req.DefaultRevision) != "" {
			targetRev := strings.TrimSpace(req.DefaultRevision)
			if !revisionIDPattern.MatchString(targetRev) || strings.Contains(targetRev, "..") {
				writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid revision %q", targetRev)})
				return
			}
			summary, code, err := s.skillRegistry.promoteDefaultRevision(r.Context(), skillName, targetRev)
			if err != nil {
				writeBrokerJSON(w, code, map[string]string{"error": err.Error()})
				return
			}
			writeBrokerJSON(w, http.StatusOK, summary)
			return
		}

		// Mode B: Upload and verify a new (or idempotent) immutable revision zip archive.
		rev := strings.TrimSpace(req.Revision)
		if !revisionIDPattern.MatchString(rev) || strings.Contains(rev, "..") {
			writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid revision %q", rev)})
			return
		}
		if strings.TrimSpace(req.ArchiveBase64) == "" {
			writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "archive_base64 or default_revision is required"})
			return
		}
		zipBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.ArchiveBase64))
		if err != nil {
			writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid base64 archive payload"})
			return
		}
		if len(zipBytes) < 4 || len(zipBytes) > brokerclient.MaxSkillArchiveBytes || !bytes.HasPrefix(zipBytes, []byte("PK\x03\x04")) {
			writeBrokerJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or oversized ZIP archive"})
			return
		}
		sum := sha256.Sum256(zipBytes)
		actualSHA := hex.EncodeToString(sum[:])
		if expected := strings.ToLower(strings.TrimSpace(req.SHA256)); expected != "" {
			if !sha256HexPattern.MatchString(expected) || subtle.ConstantTimeCompare([]byte(actualSHA), []byte(expected)) != 1 {
				s.skillRegistry.emitPolicy("Skill Registry (/api/catalog/skills)", fmt.Sprintf("PublishRevision (%s@%s)", skillName, rev), "DENY",
					fmt.Sprintf("Rejected archive upload: SHA-256 mismatch (got %s..., expected %s...)", shortSHA(actualSHA), shortSHA(expected)))
				writeBrokerJSON(w, http.StatusBadRequest, map[string]string{
					"error": fmt.Sprintf("SHA-256 digest mismatch: got %s, expected %s", actualSHA, expected),
				})
				return
			}
		}

		setDefault := true
		if req.SetDefault != nil {
			setDefault = *req.SetDefault
		}
		summary, err := s.skillRegistry.upsertRevision(skillName, rev, actualSHA, zipBytes, setDefault, req.CloudResource, true)
		if err != nil {
			code := http.StatusBadRequest
			if errors.Is(err, errRevisionImmutableConflict) {
				code = http.StatusConflict
			}
			s.skillRegistry.emitPolicy("Skill Registry (/api/catalog/skills)", fmt.Sprintf("PublishRevision (%s@%s)", skillName, rev), "DENY", err.Error())
			writeBrokerJSON(w, code, map[string]string{"error": err.Error()})
			return
		}
		writeBrokerJSON(w, http.StatusOK, summary)

	default:
		writeBrokerJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

var errRevisionImmutableConflict = errors.New("skill revision is immutable and already exists with a different SHA-256 digest")

func (p *skillRegistryProxy) upsertRevision(
	skillName, rev, shaHex string,
	zipBytes []byte,
	setDefault bool,
	cloudResource string,
	emitEvent bool,
) (CatalogSkillSummary, error) {
	fm, instructions, fileList, err := inspectAndValidateSkillZip(zipBytes, skillName)
	if err != nil {
		return CatalogSkillSummary{}, err
	}
	resName := strings.TrimSpace(cloudResource)
	if resName == "" {
		resName = p.revisionResourceName(skillName, rev)
	}
	now := time.Now().UTC()

	p.mu.Lock()
	rec, exists := p.skills[skillName]
	if !exists {
		rec = &storedSkillRecord{
			name:            skillName,
			displayName:     skillName,
			description:     fm.Description,
			defaultRevision: rev,
			updatedAt:       now,
			revisions:       make(map[string]*storedSkillRevision),
		}
		p.skills[skillName] = rec
		p.skillOrder = append(p.skillOrder, skillName)
	}
	if prev, hasRev := rec.revisions[rev]; hasRev {
		if prev.sha256 != shaHex {
			p.mu.Unlock()
			return CatalogSkillSummary{}, fmt.Errorf("%w: %s@%s", errRevisionImmutableConflict, skillName, rev)
		}
	} else {
		rec.revisions[rev] = &storedSkillRevision{
			revision:     rev,
			resourceName: resName,
			sha256:       shaHex,
			zipBytes:     append([]byte(nil), zipBytes...),
			files:        fileList,
			frontmatter:  fm,
			instructions: instructions,
			createdAt:    now,
		}
		rec.revisionOrder = append(rec.revisionOrder, rev)
	}
	if setDefault || rec.defaultRevision == "" {
		rec.defaultRevision = rev
		rec.description = fm.Description
	}
	rec.updatedAt = now
	summary := p.buildSummaryLocked(rec)
	p.mu.Unlock()

	if emitEvent {
		p.notifyMutated()
		p.emitPolicy("Skill Registry (/api/catalog/skills)", fmt.Sprintf("PublishRevision (%s@%s)", skillName, rev), "ALLOW",
			fmt.Sprintf("Verified zip (%d bytes, sha256=%s...); defaultRevision=%s", len(zipBytes), shortSHA(shaHex), summary.DefaultRevision))
	}
	return summary, nil
}

func (p *skillRegistryProxy) promoteDefaultRevision(ctx context.Context, skillName, rev string) (CatalogSkillSummary, int, error) {
	if p.cloudEnabled() {
		p.patchCloudDefaultRevision(ctx, skillName, rev)
	}

	p.mu.Lock()
	rec, ok := p.skills[skillName]
	if !ok {
		p.mu.Unlock()
		return CatalogSkillSummary{}, http.StatusNotFound, fmt.Errorf("skill %q not found", skillName)
	}
	revObj, ok := rec.revisions[rev]
	if !ok {
		p.mu.Unlock()
		return CatalogSkillSummary{}, http.StatusNotFound, fmt.Errorf("revision %q not found on skill %q", rev, skillName)
	}
	rec.defaultRevision = rev
	rec.description = revObj.frontmatter.Description
	rec.updatedAt = time.Now().UTC()
	summary := p.buildSummaryLocked(rec)
	p.mu.Unlock()
	p.notifyMutated()

	p.emitPolicy("Skill Registry (/api/catalog/skills)", fmt.Sprintf("SetDefaultRevision (%s -> %s)", skillName, rev), "ALLOW",
		fmt.Sprintf("Promoted defaultRevision=%s (sha256=%s...) with zero container rebuild", rev, shortSHA(revObj.sha256)))
	return summary, http.StatusOK, nil
}

func inspectAndValidateSkillZip(zipBytes []byte, expectedSkill string) (SkillFrontmatterDTO, string, []string, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return SkillFrontmatterDTO{}, "", nil, fmt.Errorf("open zip archive: %w", err)
	}
	if len(zr.File) > brokerclient.MaxSkillArchiveFiles {
		return SkillFrontmatterDTO{}, "", nil, fmt.Errorf("zip archive exceeds %d entry limit", brokerclient.MaxSkillArchiveFiles)
	}

	var (
		totalUncompressed int64
		files             []string
		seen              = map[string]bool{}
		skillMD           []byte
	)
	for _, zf := range zr.File {
		rawName := zf.Name
		if strings.Contains(rawName, "\\") || strings.HasPrefix(rawName, "/") || rawName == "" {
			return SkillFrontmatterDTO{}, "", nil, fmt.Errorf("invalid archive path %q", rawName)
		}
		if zf.Mode()&os.ModeSymlink != 0 {
			return SkillFrontmatterDTO{}, "", nil, fmt.Errorf("symlink archive entry %q is forbidden", rawName)
		}
		if zf.FileInfo().IsDir() {
			cleanDir := path.Clean(strings.TrimSuffix(rawName, "/"))
			if cleanDir == ".." || strings.HasPrefix(cleanDir, "../") {
				return SkillFrontmatterDTO{}, "", nil, fmt.Errorf("zip-slip directory traversal in %q", rawName)
			}
			continue
		}
		if !zf.Mode().IsRegular() {
			return SkillFrontmatterDTO{}, "", nil, fmt.Errorf("irregular archive entry %q is forbidden", rawName)
		}

		cleanRel := path.Clean(rawName)
		if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, "../") {
			return SkillFrontmatterDTO{}, "", nil, fmt.Errorf("zip-slip path traversal in %q", rawName)
		}
		for _, seg := range strings.Split(cleanRel, "/") {
			if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
				return SkillFrontmatterDTO{}, "", nil, fmt.Errorf("disallowed path segment in %q", rawName)
			}
		}
		if !isPermittedSkillEntry(cleanRel) {
			return SkillFrontmatterDTO{}, "", nil, fmt.Errorf("entry %q must be SKILL.md or inside scripts/, references/, or assets/", rawName)
		}
		if seen[cleanRel] {
			return SkillFrontmatterDTO{}, "", nil, fmt.Errorf("duplicate archive entry %q", cleanRel)
		}
		seen[cleanRel] = true

		rem := int64(brokerclient.MaxSkillArchiveBytes) - totalUncompressed
		rc, err := zf.Open()
		if err != nil {
			return SkillFrontmatterDTO{}, "", nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, rem+1))
		_ = rc.Close()
		if readErr != nil {
			return SkillFrontmatterDTO{}, "", nil, readErr
		}
		totalUncompressed += int64(len(data))
		if totalUncompressed > brokerclient.MaxSkillArchiveBytes {
			return SkillFrontmatterDTO{}, "", nil, fmt.Errorf("uncompressed skill archive exceeds %d byte cap", brokerclient.MaxSkillArchiveBytes)
		}
		if cleanRel == "SKILL.md" {
			skillMD = data
		}
		files = append(files, cleanRel)
	}
	sort.Strings(files)

	if skillMD == nil {
		return SkillFrontmatterDTO{}, "", nil, errors.New("skill archive missing SKILL.md")
	}
	fm, instructions, err := skill.ParseBytes(skillMD)
	if err != nil {
		return SkillFrontmatterDTO{}, "", nil, fmt.Errorf("invalid SKILL.md frontmatter: %w", err)
	}
	if fm.Name != expectedSkill {
		return SkillFrontmatterDTO{}, "", nil, fmt.Errorf("SKILL.md frontmatter name %q does not match skill %q", fm.Name, expectedSkill)
	}

	return SkillFrontmatterDTO{
		Name:          fm.Name,
		Description:   fm.Description,
		License:       fm.License,
		Compatibility: fm.Compatibility,
		Metadata:      fm.Metadata,
		AllowedTools:  fm.AllowedTools,
	}, strings.TrimSpace(instructions), files, nil
}

func isPermittedSkillEntry(cleanRel string) bool {
	if cleanRel == "SKILL.md" {
		return true
	}
	top, _, ok := strings.Cut(cleanRel, "/")
	if !ok {
		return false
	}
	return top == "scripts" || top == "references" || top == "assets"
}

func (p *skillRegistryProxy) listSummaries() []CatalogSkillSummary {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]CatalogSkillSummary, 0, len(p.skillOrder))
	for _, name := range p.skillOrder {
		if rec, ok := p.skills[name]; ok {
			out = append(out, p.buildSummaryLocked(rec))
		}
	}
	return out
}

func (p *skillRegistryProxy) getSummary(name string) (CatalogSkillSummary, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	rec, ok := p.skills[name]
	if !ok {
		return CatalogSkillSummary{}, false
	}
	return p.buildSummaryLocked(rec), true
}

func (p *skillRegistryProxy) getRevision(name, rev string) (*storedSkillRevision, CatalogSkillRevision, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	rec, ok := p.skills[name]
	if !ok {
		return nil, CatalogSkillRevision{}, false
	}
	revRec, ok := rec.revisions[rev]
	if !ok {
		return nil, CatalogSkillRevision{}, false
	}
	return revRec, buildRevisionDTOLocked(revRec, rec.defaultRevision == rev), true
}

func (p *skillRegistryProxy) buildSummaryLocked(rec *storedSkillRecord) CatalogSkillSummary {
	revs := make([]CatalogSkillRevision, 0, len(rec.revisionOrder))
	var activeRev *storedSkillRevision
	for _, rName := range rec.revisionOrder {
		rv, ok := rec.revisions[rName]
		if !ok {
			continue
		}
		if rName == rec.defaultRevision {
			activeRev = rv
		}
		revs = append(revs, buildRevisionDTOLocked(rv, rName == rec.defaultRevision))
	}
	var (
		activeSHA  string
		activeSize int64
		activeFM   SkillFrontmatterDTO
	)
	if activeRev != nil {
		activeSHA = activeRev.sha256
		activeSize = int64(len(activeRev.zipBytes))
		activeFM = activeRev.frontmatter
	}
	return CatalogSkillSummary{
		Name:            rec.name,
		DisplayName:     rec.displayName,
		Description:     rec.description,
		ResourceName:    p.skillResourceName(rec.name),
		DefaultRevision: rec.defaultRevision,
		DefaultRevURI:   p.revisionResourceName(rec.name, rec.defaultRevision),
		ActiveSHA256:    activeSHA,
		SHA256:          activeSHA,
		ActiveSizeBytes: activeSize,
		State:           "STATE_ACTIVE",
		Frontmatter:     activeFM,
		UpdatedAt:       rec.updatedAt.Format(time.RFC3339),
		Revisions:       revs,
	}
}

func buildRevisionDTOLocked(rv *storedSkillRevision, active bool) CatalogSkillRevision {
	preview := rv.instructions
	if len(preview) > 600 {
		preview = preview[:600] + "..."
	}
	return CatalogSkillRevision{
		Revision:            rv.revision,
		ResourceName:        rv.resourceName,
		SHA256:              rv.sha256,
		SHA256Hash:          rv.sha256,
		SizeBytes:           int64(len(rv.zipBytes)),
		State:               "ACTIVE",
		Active:              active,
		Files:               append([]string(nil), rv.files...),
		Frontmatter:         rv.frontmatter,
		InstructionsPreview: preview,
		CreatedAt:           rv.createdAt.Format(time.RFC3339),
	}
}

// Cloud agentregistry.googleapis.com/v1 synchronization helpers.

type cloudSkillResource struct {
	Name            string `json:"name"`
	DisplayName     string `json:"displayName"`
	Description     string `json:"description"`
	DefaultRevision string `json:"defaultRevision"`
	State           string `json:"state"`
}

type cloudRevisionResource struct {
	Name       string `json:"name"`
	SHA256Hash string `json:"sha256Hash"`
	SizeBytes  int64  `json:"sizeBytes"`
	State      string `json:"state"`
}

func logicalSkillID(cloudSkill string) string {
	return strings.TrimPrefix(strings.TrimSpace(cloudSkill), "private-")
}

func cloudSkillCandidates(skillName string) []string {
	clean := strings.TrimPrefix(strings.TrimSpace(skillName), "private-")
	priv := "private-" + clean
	if priv == skillName {
		return []string{priv, clean}
	}
	return []string{priv, skillName}
}

func cloudRevisionID(rev string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(rev)) {
		switch {
		case r == '.' || r == '_':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	clean := b.String()
	if len(clean) > 0 && len(clean) < 4 {
		return "rev-" + clean
	}
	return clean
}

func cloudRevisionCandidates(rev string) []string {
	c := cloudRevisionID(rev)
	if c == rev {
		return []string{rev}
	}
	return []string{c, rev}
}

func logicalRevisionID(cloudRev string) string {
	if trimmed, ok := strings.CutPrefix(strings.TrimSpace(cloudRev), "rev-"); ok && len(trimmed) > 0 && len(trimmed) < 4 {
		return trimmed
	}
	return strings.TrimSpace(cloudRev)
}

func (p *skillRegistryProxy) syncFromCloudList(ctx context.Context) {
	if !p.cloudEnabled() {
		return
	}
	listURL := fmt.Sprintf("%s/v1/projects/%s/locations/%s/skills", p.cloudBaseURL(), p.projectID, p.location)
	var out struct {
		Skills []cloudSkillResource `json:"skills"`
	}
	if err := p.cloudGetJSON(ctx, listURL, &out); err != nil {
		return
	}
	for _, sk := range out.Skills {
		cloudSkill := tailAfter(sk.Name, "/skills/")
		skillName := logicalSkillID(cloudSkill)
		cloudRev := tailAfter(sk.DefaultRevision, "/revisions/")
		defRev := logicalRevisionID(cloudRev)
		if skillIDPattern.MatchString(cloudSkill) && skillIDPattern.MatchString(skillName) && revisionIDPattern.MatchString(cloudRev) && revisionIDPattern.MatchString(defRev) {
			p.syncCloudRevisionByIDs(ctx, skillName, cloudSkill, defRev, cloudRev, true)
		}
	}
}

func (p *skillRegistryProxy) syncFromCloudSkill(ctx context.Context, skillName string) {
	if !p.cloudEnabled() {
		return
	}
	for _, cloudSkill := range cloudSkillCandidates(skillName) {
		skillURL := fmt.Sprintf("%s/v1/projects/%s/locations/%s/skills/%s", p.cloudBaseURL(), p.projectID, p.location, url.PathEscape(cloudSkill))
		var sk cloudSkillResource
		if err := p.cloudGetJSON(ctx, skillURL, &sk); err != nil {
			continue
		}
		cloudRev := tailAfter(sk.DefaultRevision, "/revisions/")
		defRev := logicalRevisionID(cloudRev)
		if revisionIDPattern.MatchString(cloudRev) && revisionIDPattern.MatchString(defRev) {
			p.syncCloudRevisionByIDs(ctx, logicalSkillID(skillName), cloudSkill, defRev, cloudRev, true)
		}
		return
	}
}

func (p *skillRegistryProxy) syncFromCloudRevision(ctx context.Context, skillName, rev string) {
	if !p.cloudEnabled() {
		return
	}
	logicalSkill := logicalSkillID(skillName)
	logicalRev := logicalRevisionID(rev)
	p.mu.RLock()
	_, exists := p.skills[logicalSkill]
	hasRev := false
	if exists {
		_, hasRev = p.skills[logicalSkill].revisions[logicalRev]
	}
	p.mu.RUnlock()
	if !hasRev {
		for _, cloudSkill := range cloudSkillCandidates(logicalSkill) {
			for _, cloudRev := range cloudRevisionCandidates(logicalRev) {
				if p.syncCloudRevisionByIDs(ctx, logicalSkill, cloudSkill, logicalRev, cloudRev, false) {
					return
				}
			}
		}
	}
}

func (p *skillRegistryProxy) syncCloudRevisionByIDs(ctx context.Context, skillName, cloudSkill, rev, cloudRev string, setDefault bool) bool {
	p.mu.RLock()
	if rec, ok := p.skills[skillName]; ok {
		if _, hasRev := rec.revisions[rev]; hasRev {
			p.mu.RUnlock()
			if setDefault {
				p.mu.Lock()
				rec.defaultRevision = rev
				p.mu.Unlock()
				p.notifyMutated()
			}
			return true
		}
	}
	p.mu.RUnlock()

	revPath := fmt.Sprintf("projects/%s/locations/%s/skills/%s/revisions/%s", p.projectID, p.location, url.PathEscape(cloudSkill), url.PathEscape(cloudRev))
	metaURL := fmt.Sprintf("%s/v1/%s", p.cloudBaseURL(), revPath)
	var revMeta cloudRevisionResource
	if err := p.cloudGetJSON(ctx, metaURL, &revMeta); err != nil {
		return false
	}

	zipBytes, err := p.cloudDownloadZip(ctx, revPath)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(zipBytes)
	actualSHA := hex.EncodeToString(sum[:])
	if wantSHA := strings.ToLower(strings.TrimSpace(revMeta.SHA256Hash)); sha256HexPattern.MatchString(wantSHA) && actualSHA != wantSHA {
		p.emitPolicy("Skill Registry (/agentregistry)", fmt.Sprintf("CloudSync (%s@%s)", skillName, rev), "DENY",
			"Cloud archive SHA-256 did not match SkillRevision.sha256Hash")
		return false
	}
	_, err = p.upsertRevision(skillName, rev, actualSHA, zipBytes, setDefault, revMeta.Name, true)
	return err == nil
}

func (p *skillRegistryProxy) cloudDownloadZip(ctx context.Context, revResourcePath string) ([]byte, error) {
	candidates := []string{
		fmt.Sprintf("%s/download/v1/%s?alt=media", p.cloudBaseURL(), revResourcePath),
		fmt.Sprintf("%s/v1/%s?alt=media", p.cloudBaseURL(), revResourcePath),
	}
	var lastErr error
	for _, u := range candidates {
		reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u, nil)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		resp, err := p.authedClient.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, brokerclient.MaxSkillArchiveBytes+1))
		_ = resp.Body.Close()
		cancel()
		if readErr == nil && resp.StatusCode == http.StatusOK && len(data) <= brokerclient.MaxSkillArchiveBytes && bytes.HasPrefix(data, []byte("PK\x03\x04")) {
			return data, nil
		}
		lastErr = fmt.Errorf("download %s HTTP %d", u, resp.StatusCode)
	}
	return nil, lastErr
}

func (p *skillRegistryProxy) cloudGetJSON(ctx context.Context, targetURL string, out any) error {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		return err
	}
	resp, err := p.authedClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out)
}

func (p *skillRegistryProxy) patchCloudDefaultRevision(ctx context.Context, skillName, rev string) {
	logicalSkill := logicalSkillID(skillName)
	logicalRev := logicalRevisionID(rev)
	for _, cloudSkill := range cloudSkillCandidates(logicalSkill) {
		for _, cloudRev := range cloudRevisionCandidates(logicalRev) {
			reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			targetURL := fmt.Sprintf(
				"%s/v1/projects/%s/locations/%s/skills/%s?updateMask=defaultRevision",
				p.cloudBaseURL(), p.projectID, p.location, url.PathEscape(cloudSkill),
			)
			body, _ := json.Marshal(map[string]string{
				"defaultRevision": p.revisionResourceName(cloudSkill, cloudRev),
			})
			req, err := http.NewRequestWithContext(reqCtx, http.MethodPatch, targetURL, bytes.NewReader(body))
			if err != nil {
				cancel()
				continue
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := p.authedClient.Do(req)
			if err != nil {
				cancel()
				continue
			}
			code := resp.StatusCode
			_ = resp.Body.Close()
			cancel()
			if code >= 200 && code < 300 {
				return
			}
		}
	}
}

func (p *skillRegistryProxy) emitPolicy(boundary, action, verdict, detail string) {
	if p.recordPolicy == nil {
		return
	}
	p.recordPolicy(PolicyEvent{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Boundary:  boundary,
		Action:    action,
		Verdict:   verdict,
		Detail:    detail,
	})
}

func buildCatalogCapabilities(skills []CatalogSkillSummary) []CatalogCapabilityEntry {
	skillCap := "ledger-pool-triage (v1)"
	skillStage := "Module 03 (active)"
	if len(skills) > 0 {
		s := skills[0]
		skillCap = fmt.Sprintf("%s (default=%s · sha256:%s)", s.Name, s.DefaultRevision, shortSHA(s.ActiveSHA256))
	}
	return []CatalogCapabilityEntry{
		{
			Capability: "clone_repo",
			Kind:       "ADK functiontool",
			Boundary:   "Routes Smart HTTP git clone strictly via sre-gateway/ops",
			Stage:      "Module 01 (active)",
		},
		{
			Capability: "read_workspace_file",
			Kind:       "ADK functiontool",
			Boundary:   "Confined to /workspace with filepath.Clean + symlink rejection",
			Stage:      "Module 01 (active)",
		},
		{
			Capability: "run_workspace_command",
			Kind:       "ADK functiontool",
			Boundary:   "Allowlists ledger-verify, git status/diff/log, and .skills/*.sh",
			Stage:      "Module 01 (active)",
		},
		{
			Capability: "stage_git_patch",
			Kind:       "ADK functiontool",
			Boundary:   "Runs ledger-verify AST + YAML checks before committing branch",
			Stage:      "Module 01 (active)",
		},
		{
			Capability: "load_memory & preload_memory",
			Kind:       "Gemini Sessions & Memory Bank",
			Boundary:   "Keyless /agentengine proxy + zero-wake Session Ledger & cross-rollout recall",
			Stage:      "Module 02 (active)",
		},
		{
			Capability: skillCap,
			Kind:       "Procedural Skill (Skill Registry)",
			Boundary:   "SHA-256 archive verification + atomic .skills/ unpack with zero Golden Snapshot churn",
			Stage:      skillStage,
		},
		{
			Capability: "push_git_branch & apply_remediation_pr",
			Kind:       "Remediation Gate (ADK toolconfirmation)",
			Boundary:   "0 vCPU Cloud Storage suspend + human operator approval before merge",
			Stage:      "Module 04 (active)",
		},
		{
			Capability: "Cloud Logging, GKE & ops-hub MCP",
			Kind:       "Agent Registry MCP & A2A",
			Boundary:   "readOnlyHint / destructiveHint enforcement + A2A Agent Card",
			Stage:      "Module 04 (active)",
		},
		{
			Capability: "Model Armor & inline SDP",
			Kind:       "Zero-trust content screening",
			Boundary:   "SanitizeUserPrompt & SanitizeModelResponse hard-block INC-409 prompt injection + redact PII",
			Stage:      "Module 05 (active)",
		},
		{
			Capability: "Pod Agent Identity & 3LO Auth Manager",
			Kind:       "SPIFFE SVID + adk/v2/auth/gcp PKCE",
			Boundary:   "Server-side 15m in-memory 3LO vault + dual operator/agent PR attribution (0 sandbox tokens)",
			Stage:      "Module 05 (active)",
		},
		{
			Capability: "OpenTelemetry GenAI waterfall & Online Evals",
			Kind:       "Cloud Trace + GMP PodMonitoring + Gemini autorater",
			Boundary:   "W3C traceparent + secret-scrubbed /otlp/v1/traces + 4-rubric pure-Go online evals",
			Stage:      "Module 06 (active)",
		},
	}
}

func tailAfter(full, marker string) string {
	v := strings.TrimSpace(full)
	if idx := strings.LastIndex(v, marker); idx >= 0 {
		return v[idx+len(marker):]
	}
	return v
}

func shortSHA(sha string) string {
	if len(sha) >= 12 {
		return sha[:12]
	}
	return sha
}

const seededLedgerPoolTriageV1SkillMD = `---
name: ledger-pool-triage
description: Diagnose connection pool sizing and idle timeout saturation risks in ledger-api (config/pool.yaml).
license: Apache-2.0
compatibility: POSIX sh inside /workspace/ledger-api
metadata:
  revision: v1
  service: ledger-api
allowed-tools: clone_repo read_workspace_file run_workspace_command
---

# Ledger Pool Triage Runbook (Revision v1)

Follow these exact steps inside the Incident Actor sandbox to run the baseline connection pool diagnostic against ` + "`ledger-api`" + `:

1. Ensure the ` + "`ledger-api`" + ` repository is cloned into ` + "`/workspace/ledger-api`" + ` by calling ` + "`clone_repo`" + ` (` + "`repo=\"ledger-api\"`" + `).
2. Execute the unpacked Procedural Skill diagnostic script via ` + "`run_workspace_command`" + `:
   - ` + "`command`" + `: ` + "`\"sh\"`" + `
   - ` + "`args`" + `: ` + "`[\".skills/ledger-pool-triage/scripts/diagnose_pool.sh\"]`" + `
   - ` + "`dir`" + `: ` + "`\"ledger-api\"`" + `
3. Report the ` + "`SKILL_REVISION=v1`" + ` key-value and JSON output, specifically ` + "`MAX_OPEN_CONNS`" + `, ` + "`IDLE_TIMEOUT_SECONDS`" + `, ` + "`ACQUIRE_TIMEOUT_MS`" + `, and ` + "`POOL_CONFIG_STATUS`" + `.
`

const seededLedgerPoolTriageV1Script = `#!/bin/sh
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Baseline ledger-api connection pool diagnostic (SKILL_REVISION=v1).
# Inspects config/pool.yaml inside the working directory (or $1).
set -eu

TARGET_DIR="${1:-.}"
POOL_YAML="${TARGET_DIR}/config/pool.yaml"

if [ ! -f "${POOL_YAML}" ]; then
  echo "SKILL_NAME=ledger-pool-triage"
  echo "SKILL_REVISION=v1"
  echo "POOL_CONFIG_STATUS=MISSING_POOL_YAML"
  printf '{"skill":"ledger-pool-triage","skill_revision":"v1","error":"missing %s"}\n' "${POOL_YAML}"
  exit 1
fi

read_yaml_int() {
  key="$1"
  awk -F: -v k="$key" '
    {
      sub(/#.*/, "", $0)
      gsub(/^[ \t]+|[ \t]+$/, "", $1)
      if ($1 == k) {
        gsub(/^[ \t]+|[ \t]+$/, "", $2)
        print $2
        exit
      }
    }
  ' "${POOL_YAML}"
}

MAX_OPEN="$(read_yaml_int max_open_conns)"
IDLE_TIMEOUT="$(read_yaml_int idle_timeout_seconds)"
ACQUIRE_MS="$(read_yaml_int acquire_timeout_ms)"

: "${MAX_OPEN:=0}"
: "${IDLE_TIMEOUT:=0}"
: "${ACQUIRE_MS:=0}"

POOL_STATUS="OK"
POOL_OK="true"
if [ "${MAX_OPEN}" -lt 16 ] || [ "${IDLE_TIMEOUT}" -lt 5 ]; then
  POOL_STATUS="SATURATION_RISK"
  POOL_OK="false"
fi

echo "SKILL_NAME=ledger-pool-triage"
echo "SKILL_REVISION=v1"
echo "CHECK_SCOPE=pool_config_baseline"
echo "MAX_OPEN_CONNS=${MAX_OPEN}"
echo "IDLE_TIMEOUT_SECONDS=${IDLE_TIMEOUT}"
echo "ACQUIRE_TIMEOUT_MS=${ACQUIRE_MS}"
echo "POOL_CONFIG_STATUS=${POOL_STATUS}"
printf '{"skill":"ledger-pool-triage","skill_revision":"v1","scope":"pool_config_baseline","max_open_conns":%s,"idle_timeout_seconds":%s,"acquire_timeout_ms":%s,"pool_config_ok":%s}\n' \
  "${MAX_OPEN}" "${IDLE_TIMEOUT}" "${ACQUIRE_MS}" "${POOL_OK}"
`
