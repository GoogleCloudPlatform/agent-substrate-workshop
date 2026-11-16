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

package brokerclient

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/telemetry"
	"google.golang.org/adk/v2/tool/skilltoolset/skill"
)

const (
	// MaxSkillArchiveBytes caps both compressed zip downloads and total extracted bytes.
	MaxSkillArchiveBytes = 10 << 20 // 10 MiB
	// MaxSkillArchiveFiles caps the number of entries permitted in a skill archive.
	MaxSkillArchiveFiles = 64
)

var (
	validRevisionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	validSHA256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// RegistrySkillSource implements ADK Go v2's skill.Source over keyless HTTP routes
// exposed by the Governed Broker (/agentregistry/skills). Each skill's active
// defaultRevision archive is verified by SHA-256 before atomic extraction into
// <workspaceDir>/.skills/<name>@<revision>/ and <workspaceDir>/.skills/<name>/.
type RegistrySkillSource struct {
	baseURL    string
	skillsRoot string
	hc         *http.Client

	mu sync.Mutex
}

// NewSkillSource constructs a keyless RegistrySkillSource rooted at <workspaceDir>/.skills.
func NewSkillSource(brokerURL, workspaceDir string, hc *http.Client) (*RegistrySkillSource, error) {
	base := strings.TrimRight(strings.TrimSpace(brokerURL), "/")
	if base == "" {
		return nil, errors.New("brokerclient: BROKER_URL is required")
	}
	ws := strings.TrimSpace(workspaceDir)
	if ws == "" {
		ws = "/workspace"
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return nil, fmt.Errorf("brokerclient: create workspace directory %s: %w", ws, err)
	}
	absWS, err := filepath.Abs(filepath.Clean(ws))
	if err != nil {
		return nil, fmt.Errorf("brokerclient: resolve workspace path: %w", err)
	}
	evalWS, err := filepath.EvalSymlinks(absWS)
	if err != nil {
		return nil, fmt.Errorf("brokerclient: evaluate workspace symlinks: %w", err)
	}
	skillsRoot := filepath.Join(evalWS, ".skills")
	if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
		return nil, fmt.Errorf("brokerclient: create skills directory %s: %w", skillsRoot, err)
	}
	hc = telemetry.WrapHTTPClient(hc, 30*time.Second)
	return &RegistrySkillSource{
		baseURL:    base,
		skillsRoot: skillsRoot,
		hc:         hc,
	}, nil
}

type wireSkillSummary struct {
	Name            string `json:"name"`
	DefaultRevision string `json:"default_revision"`
	DefaultRevCamel string `json:"defaultRevision"`
	SHA256          string `json:"sha256"`
	SHA256Hash      string `json:"sha256Hash"`
}

func (w wireSkillSummary) effectiveRevision() string {
	if r := strings.TrimSpace(w.DefaultRevision); r != "" {
		return trimRevisionResource(r)
	}
	return trimRevisionResource(strings.TrimSpace(w.DefaultRevCamel))
}

func (w wireSkillSummary) effectiveSHA256() string {
	if s := strings.ToLower(strings.TrimSpace(w.SHA256)); s != "" {
		return s
	}
	return strings.ToLower(strings.TrimSpace(w.SHA256Hash))
}

func trimRevisionResource(v string) string {
	if idx := strings.LastIndex(v, "/revisions/"); idx >= 0 {
		return v[idx+len("/revisions/"):]
	}
	return v
}

func trimSkillResource(v string) string {
	if idx := strings.LastIndex(v, "/skills/"); idx >= 0 {
		return v[idx+len("/skills/"):]
	}
	return v
}

// ListFrontmatters lists active skills from the Governed Broker, ensures each
// active defaultRevision is SHA-256-verified and unpacked under .skills/, and
// returns parsed SKILL.md frontmatters.
func (s *RegistrySkillSource) ListFrontmatters(ctx context.Context) ([]*skill.Frontmatter, error) {
	var resp struct {
		Skills []wireSkillSummary `json:"skills"`
	}
	statusCode, err := s.getJSON(ctx, "/agentregistry/skills", &resp)
	if err != nil {
		return nil, fmt.Errorf("brokerclient: list skills (HTTP %d): %w", statusCode, err)
	}

	out := make([]*skill.Frontmatter, 0, len(resp.Skills))
	for _, item := range resp.Skills {
		skillName := trimSkillResource(strings.TrimSpace(item.Name))
		if err := validateSkillName(skillName); err != nil {
			return nil, err
		}
		item.Name = skillName
		dir, err := s.ensureRevisionUnpacked(ctx, item)
		if err != nil {
			return nil, err
		}
		fm, _, err := readUnpackedSkillMD(dir, skillName)
		if err != nil {
			return nil, err
		}
		out = append(out, fm)
	}
	return out, nil
}

// LoadFrontmatter resolves the active defaultRevision for name, verifies and
// unpacks its zip archive if needed, and returns its SKILL.md Frontmatter.
func (s *RegistrySkillSource) LoadFrontmatter(ctx context.Context, name string) (*skill.Frontmatter, error) {
	dir, err := s.syncActiveSkill(ctx, name)
	if err != nil {
		return nil, err
	}
	fm, _, err := readUnpackedSkillMD(dir, name)
	return fm, err
}

// LoadInstructions resolves the active defaultRevision for name, verifies and
// unpacks its zip archive if needed, and returns its SKILL.md Markdown body.
func (s *RegistrySkillSource) LoadInstructions(ctx context.Context, name string) (string, error) {
	dir, err := s.syncActiveSkill(ctx, name)
	if err != nil {
		return "", err
	}
	_, body, err := readUnpackedSkillMD(dir, name)
	return body, err
}

// LoadResource opens a resource file inside references/, assets/, or scripts/
// for the skill's active defaultRevision.
func (s *RegistrySkillSource) LoadResource(ctx context.Context, name, resourcePath string) (io.ReadCloser, error) {
	if err := validateSkillName(name); err != nil {
		return nil, err
	}
	cleanRel, err := validateSkillResourceFilePath(resourcePath)
	if err != nil {
		return nil, err
	}
	dir, err := s.syncActiveSkill(ctx, name)
	if err != nil {
		return nil, err
	}
	full, err := resolveSafeUnder(dir, cleanRel)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", skill.ErrInvalidResourcePath, err)
	}
	st, err := os.Lstat(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %q", skill.ErrResourceNotFound, cleanRel)
		}
		return nil, fmt.Errorf("stat skill resource %q: %w", cleanRel, err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %q is not a regular file", skill.ErrInvalidResourcePath, cleanRel)
	}
	if st.Size() > MaxSkillArchiveBytes {
		return nil, fmt.Errorf("skill resource %q exceeds %d byte cap", cleanRel, MaxSkillArchiveBytes)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("read skill resource %q: %w", cleanRel, err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// ListResources returns sorted resource paths relative to the skill root under
// references/, assets/, and scripts/ for the skill's active defaultRevision.
func (s *RegistrySkillSource) ListResources(ctx context.Context, name, subpath string) ([]string, error) {
	if err := validateSkillName(name); err != nil {
		return nil, err
	}
	targets, isRoot, cleanSub, err := validateSkillResourceDirPath(subpath)
	if err != nil {
		return nil, err
	}
	dir, err := s.syncActiveSkill(ctx, name)
	if err != nil {
		return nil, err
	}
	if !isRoot {
		subFull, err := resolveSafeUnder(dir, cleanSub)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", skill.ErrInvalidResourcePath, err)
		}
		if _, err := os.Lstat(subFull); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("%w: %q", skill.ErrResourceNotFound, cleanSub)
			}
			return nil, err
		}
	}

	var out []string
	for _, target := range targets {
		targetFull, err := resolveSafeUnder(dir, target)
		if err != nil {
			continue
		}
		st, err := os.Lstat(targetFull)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if !st.IsDir() {
			if st.Mode().IsRegular() {
				out = append(out, target)
			}
			continue
		}
		walkErr := filepath.WalkDir(targetFull, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("%w: symlink not allowed in skill directory", skill.ErrInvalidResourcePath)
			}
			if !d.IsDir() {
				rel, relErr := filepath.Rel(dir, p)
				if relErr == nil {
					out = append(out, filepath.ToSlash(rel))
				}
			}
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *RegistrySkillSource) syncActiveSkill(ctx context.Context, name string) (string, error) {
	if err := validateSkillName(name); err != nil {
		return "", err
	}
	var meta wireSkillSummary
	statusCode, err := s.getJSON(ctx, "/agentregistry/skills/"+url.PathEscape(name), &meta)
	if err != nil {
		if statusCode == http.StatusNotFound {
			return "", fmt.Errorf("%w: %q", skill.ErrSkillNotFound, name)
		}
		return "", fmt.Errorf("brokerclient: fetch skill %q metadata (HTTP %d): %w", name, statusCode, err)
	}
	meta.Name = name
	return s.ensureRevisionUnpacked(ctx, meta)
}

func (s *RegistrySkillSource) ensureRevisionUnpacked(ctx context.Context, meta wireSkillSummary) (string, error) {
	rev := meta.effectiveRevision()
	if !validRevisionPattern.MatchString(rev) || strings.Contains(rev, "..") {
		return "", fmt.Errorf("brokerclient: invalid revision identifier %q for skill %q", rev, meta.Name)
	}
	expectedSHA := meta.effectiveSHA256()
	if !validSHA256Pattern.MatchString(expectedSHA) {
		return "", fmt.Errorf("brokerclient: invalid or missing SHA-256 digest %q for %s@%s", expectedSHA, meta.Name, rev)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	versionedDir := filepath.Join(s.skillsRoot, meta.Name+"@"+rev)
	activeDir := filepath.Join(s.skillsRoot, meta.Name)
	prefix := s.skillsRoot + string(filepath.Separator)
	if !strings.HasPrefix(versionedDir, prefix) || !strings.HasPrefix(activeDir, prefix) {
		return "", fmt.Errorf("brokerclient: resolved skill directory escapes .skills boundary")
	}

	versionedMarker := filepath.Join(versionedDir, ".sha256")
	needUnpack := true
	if rawSHA, err := os.ReadFile(versionedMarker); err == nil {
		if strings.TrimSpace(string(rawSHA)) == expectedSHA {
			if _, statErr := os.Stat(filepath.Join(versionedDir, "SKILL.md")); statErr == nil {
				needUnpack = false
			}
		}
	}

	if needUnpack {
		zipBytes, err := s.downloadArchive(ctx, meta.Name, rev)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(zipBytes)
		actualSHA := hex.EncodeToString(sum[:])
		if subtle.ConstantTimeCompare([]byte(actualSHA), []byte(expectedSHA)) != 1 {
			return "", fmt.Errorf(
				"brokerclient: SHA-256 mismatch for skill %s@%s: got %s, expected %s",
				meta.Name, rev, actualSHA, expectedSHA,
			)
		}

		files, err := extractVerifiedSkillFiles(zipBytes, meta.Name)
		if err != nil {
			return "", err
		}

		if err := writeSkillDirAtomically(s.skillsRoot, versionedDir, files, map[string]string{
			".sha256":   expectedSHA + "\n",
			".revision": rev + "\n",
		}); err != nil {
			return "", err
		}
	}

	// Point <skillsRoot>/<name> at the active defaultRevision using an atomic
	// regular-file directory swap so Workspace.resolveInside permits script execution
	// without relaxing its OS symlink rejection check.
	activeRevMarker := filepath.Join(activeDir, ".revision")
	activeCurrentRev, _ := os.ReadFile(activeRevMarker)
	activeCurrentSHA, _ := os.ReadFile(filepath.Join(activeDir, ".sha256"))
	if strings.TrimSpace(string(activeCurrentRev)) != rev || strings.TrimSpace(string(activeCurrentSHA)) != expectedSHA {
		files, err := readSkillDirFiles(versionedDir)
		if err != nil {
			return "", err
		}
		if err := writeSkillDirAtomically(s.skillsRoot, activeDir, files, map[string]string{
			".sha256":   expectedSHA + "\n",
			".revision": rev + "\n",
		}); err != nil {
			return "", err
		}
	}

	return versionedDir, nil
}

type extractedSkillFile struct {
	relPath string
	data    []byte
	mode    os.FileMode
}

func extractVerifiedSkillFiles(zipBytes []byte, expectedSkillName string) ([]extractedSkillFile, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("brokerclient: open skill zip archive: %w", err)
	}
	if len(zr.File) > MaxSkillArchiveFiles {
		return nil, fmt.Errorf("brokerclient: skill archive contains %d entries, exceeding %d file limit", len(zr.File), MaxSkillArchiveFiles)
	}

	var (
		totalBytes int64
		files      []extractedSkillFile
		skillMD    []byte
		seenPaths  = map[string]bool{}
	)

	for _, zf := range zr.File {
		rawName := zf.Name
		if strings.Contains(rawName, "\\") || strings.HasPrefix(rawName, "/") || rawName == "" {
			return nil, fmt.Errorf("brokerclient: invalid archive entry path %q", rawName)
		}
		mode := zf.Mode()
		if mode&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("brokerclient: symlink archive entry %q is forbidden", rawName)
		}
		if zf.FileInfo().IsDir() {
			cleanDir := path.Clean(strings.TrimSuffix(rawName, "/"))
			if cleanDir == ".." || strings.HasPrefix(cleanDir, "../") {
				return nil, fmt.Errorf("brokerclient: zip-slip directory traversal in %q", rawName)
			}
			continue
		}
		if !mode.IsRegular() {
			return nil, fmt.Errorf("brokerclient: irregular archive entry %q (mode %v) is forbidden", rawName, mode)
		}

		cleanRel := path.Clean(rawName)
		if cleanRel == "." || cleanRel == ".." || strings.HasPrefix(cleanRel, "../") {
			return nil, fmt.Errorf("brokerclient: zip-slip path traversal in %q", rawName)
		}
		for _, seg := range strings.Split(cleanRel, "/") {
			if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
				return nil, fmt.Errorf("brokerclient: disallowed path segment in archive entry %q", rawName)
			}
		}
		if !isAllowedSkillArchiveFile(cleanRel) {
			return nil, fmt.Errorf("brokerclient: archive entry %q is outside allowed skill paths (SKILL.md, scripts/, references/, assets/)", rawName)
		}
		if seenPaths[cleanRel] {
			return nil, fmt.Errorf("brokerclient: duplicate archive entry %q", cleanRel)
		}
		seenPaths[cleanRel] = true

		remaining := int64(MaxSkillArchiveBytes) - totalBytes
		rc, err := zf.Open()
		if err != nil {
			return nil, fmt.Errorf("brokerclient: open zip entry %q: %w", cleanRel, err)
		}
		entryBytes, readErr := io.ReadAll(io.LimitReader(rc, remaining+1))
		_ = rc.Close()
		if readErr != nil {
			return nil, fmt.Errorf("brokerclient: read zip entry %q: %w", cleanRel, readErr)
		}
		totalBytes += int64(len(entryBytes))
		if totalBytes > MaxSkillArchiveBytes {
			return nil, fmt.Errorf("brokerclient: uncompressed skill archive exceeds %d byte limit", MaxSkillArchiveBytes)
		}

		perm := os.FileMode(0o644)
		if strings.HasPrefix(cleanRel, "scripts/") && strings.HasSuffix(cleanRel, ".sh") {
			perm = 0o755
		}
		if cleanRel == "SKILL.md" {
			skillMD = entryBytes
		}
		files = append(files, extractedSkillFile{
			relPath: cleanRel,
			data:    entryBytes,
			mode:    perm,
		})
	}

	if skillMD == nil {
		return nil, fmt.Errorf("%w: archive missing SKILL.md", skill.ErrInvalidFrontmatter)
	}
	fm, _, err := skill.ParseBytes(skillMD)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", skill.ErrInvalidFrontmatter, err)
	}
	if fm.Name != expectedSkillName {
		return nil, fmt.Errorf("%w: SKILL.md name %q does not match skill %q", skill.ErrInvalidSkillName, fm.Name, expectedSkillName)
	}

	return files, nil
}

func isAllowedSkillArchiveFile(cleanRel string) bool {
	if cleanRel == "SKILL.md" {
		return true
	}
	top, _, ok := strings.Cut(cleanRel, "/")
	if !ok {
		return false
	}
	switch top {
	case "scripts", "references", "assets":
		return true
	default:
		return false
	}
}

func writeSkillDirAtomically(skillsRoot, targetDir string, files []extractedSkillFile, markers map[string]string) error {
	stageName, err := randomStageSuffix()
	if err != nil {
		return err
	}
	stageDir := filepath.Join(skillsRoot, ".stage-"+stageName)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return fmt.Errorf("brokerclient: create staging dir: %w", err)
	}
	cleanupStage := true
	defer func() {
		if cleanupStage {
			_ = os.RemoveAll(stageDir)
		}
	}()

	stagePrefix := stageDir + string(filepath.Separator)
	for _, f := range files {
		dest := filepath.Join(stageDir, filepath.FromSlash(f.relPath))
		if !strings.HasPrefix(dest, stagePrefix) {
			return fmt.Errorf("brokerclient: staging path %q escaped boundary", f.relPath)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, f.data, f.mode); err != nil {
			return err
		}
		if err := os.Chmod(dest, f.mode); err != nil {
			return err
		}
	}
	for k, v := range markers {
		dest := filepath.Join(stageDir, k)
		if !strings.HasPrefix(dest, stagePrefix) {
			return fmt.Errorf("brokerclient: marker %q escaped boundary", k)
		}
		if err := os.WriteFile(dest, []byte(v), 0o644); err != nil {
			return err
		}
	}

	backupDir := ""
	if _, err := os.Lstat(targetDir); err == nil {
		bakSuffix, err := randomStageSuffix()
		if err != nil {
			return err
		}
		backupDir = filepath.Join(skillsRoot, ".old-"+bakSuffix)
		if err := os.Rename(targetDir, backupDir); err != nil {
			return fmt.Errorf("brokerclient: rotate previous skill dir: %w", err)
		}
	}
	if err := os.Rename(stageDir, targetDir); err != nil {
		if backupDir != "" {
			_ = os.Rename(backupDir, targetDir)
		}
		return fmt.Errorf("brokerclient: activate skill dir %s: %w", targetDir, err)
	}
	cleanupStage = false
	if backupDir != "" {
		_ = os.RemoveAll(backupDir)
	}
	return nil
}

func readSkillDirFiles(dir string) ([]extractedSkillFile, error) {
	var out []extractedSkillFile
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == dir {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("brokerclient: unexpected symlink in unpacked skill directory")
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		slashRel := filepath.ToSlash(rel)
		if strings.HasPrefix(slashRel, ".") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info.Mode()&0o111 != 0 || (strings.HasPrefix(slashRel, "scripts/") && strings.HasSuffix(slashRel, ".sh")) {
			mode = 0o755
		}
		out = append(out, extractedSkillFile{
			relPath: slashRel,
			data:    data,
			mode:    mode,
		})
		return nil
	})
	return out, err
}

func readUnpackedSkillMD(dir, expectedName string) (*skill.Frontmatter, string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", fmt.Errorf("%w: %q", skill.ErrSkillNotFound, expectedName)
		}
		return nil, "", err
	}
	fm, body, err := skill.ParseBytes(raw)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", skill.ErrInvalidFrontmatter, err)
	}
	if fm.Name != expectedName {
		return nil, "", fmt.Errorf("%w: SKILL.md name %q does not match %q", skill.ErrInvalidSkillName, fm.Name, expectedName)
	}
	return fm, body, nil
}

func (s *RegistrySkillSource) downloadArchive(ctx context.Context, name, rev string) ([]byte, error) {
	u := fmt.Sprintf(
		"%s/agentregistry/skills/%s/revisions/%s?alt=media",
		s.baseURL,
		url.PathEscape(name),
		url.PathEscape(rev),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brokerclient: download skill %s@%s: %w", name, rev, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s@%s", skill.ErrSkillNotFound, name, rev)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("brokerclient: download skill %s@%s HTTP %d: %s", name, rev, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxSkillArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("brokerclient: read skill archive %s@%s: %w", name, rev, err)
	}
	if len(data) > MaxSkillArchiveBytes {
		return nil, fmt.Errorf("brokerclient: skill archive %s@%s exceeds %d byte limit", name, rev, MaxSkillArchiveBytes)
	}
	return data, nil
}

func (s *RegistrySkillSource) getJSON(ctx context.Context, route string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+route, nil)
	if err != nil {
		return 0, err
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func validateSkillName(name string) error {
	if len(name) < 1 || len(name) > 64 {
		return fmt.Errorf("%w: %q must be 1..64 characters", skill.ErrInvalidSkillName, name)
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") || strings.Contains(name, "--") {
		return fmt.Errorf("%w: %q has invalid hyphen placement", skill.ErrInvalidSkillName, name)
	}
	for _, ch := range name {
		if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-') {
			return fmt.Errorf("%w: %q must contain only lowercase alphanumeric characters and hyphens", skill.ErrInvalidSkillName, name)
		}
	}
	return nil
}

func validateSkillResourceFilePath(resourcePath string) (string, error) {
	if strings.Contains(resourcePath, "\\") || strings.HasPrefix(resourcePath, "/") {
		return "", fmt.Errorf("%w: %q", skill.ErrInvalidResourcePath, resourcePath)
	}
	clean := path.Clean(resourcePath)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %q", skill.ErrInvalidResourcePath, resourcePath)
	}
	if !strings.HasPrefix(clean, "references/") && !strings.HasPrefix(clean, "assets/") && !strings.HasPrefix(clean, "scripts/") {
		return "", fmt.Errorf("%w: %q must be within 'references/', 'assets/', or 'scripts/'", skill.ErrInvalidResourcePath, resourcePath)
	}
	return clean, nil
}

func validateSkillResourceDirPath(subpath string) ([]string, bool, string, error) {
	if strings.Contains(subpath, "\\") || strings.HasPrefix(subpath, "/") {
		return nil, false, "", fmt.Errorf("%w: %q", skill.ErrInvalidResourcePath, subpath)
	}
	clean := path.Clean(subpath)
	if subpath == "" || clean == "." {
		return []string{"references", "assets", "scripts"}, true, ".", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return nil, false, "", fmt.Errorf("%w: %q", skill.ErrInvalidResourcePath, subpath)
	}
	switch strings.SplitN(clean, "/", 2)[0] {
	case "references", "assets", "scripts":
		return []string{clean}, false, clean, nil
	default:
		return nil, false, "", fmt.Errorf("%w: %q must be empty, '.', or within 'references/', 'assets/', or 'scripts/'", skill.ErrInvalidResourcePath, subpath)
	}
}

func resolveSafeUnder(rootDir, cleanSlashRel string) (string, error) {
	full := filepath.Join(rootDir, filepath.FromSlash(cleanSlashRel))
	prefix := rootDir + string(filepath.Separator)
	if !strings.HasPrefix(full, prefix) {
		return "", fmt.Errorf("path %q escapes skill root", cleanSlashRel)
	}
	cur := rootDir
	for _, part := range strings.Split(cleanSlashRel, "/") {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return "", err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink %q not permitted", cleanSlashRel)
		}
	}
	return full, nil
}

func randomStageSuffix() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

var _ skill.Source = (*RegistrySkillSource)(nil)
