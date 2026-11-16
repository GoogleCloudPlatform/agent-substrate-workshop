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
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"google.golang.org/adk/v2/session"
)

type persistedBrokerState struct {
	Version                     int                                  `json:"version"`
	DiscoveredReasoningEngineID string                               `json:"discovered_reasoning_engine_id,omitempty"`
	Sessions                    []persistedSessionRecord             `json:"sessions,omitempty"`
	AppState                    map[string]map[string]any            `json:"app_state,omitempty"`
	UserState                   map[string]map[string]map[string]any `json:"user_state,omitempty"`
	MemoriesByApp               map[string][]ConsolidatedMemoryFact  `json:"memories_by_app,omitempty"`
	Skills                      []persistedSkillRecord               `json:"skills,omitempty"`
	PendingGates                map[string]RemediationGateDTO        `json:"pending_gates,omitempty"`
}

type persistedSessionRecord struct {
	AppName        string           `json:"app_name"`
	UserID         string           `json:"user_id"`
	SessionID      string           `json:"session_id"`
	CloudSessionID string           `json:"cloud_session_id,omitempty"`
	IncidentID     string           `json:"incident_id,omitempty"`
	Template       string           `json:"template,omitempty"`
	State          map[string]any   `json:"state,omitempty"`
	Events         []*session.Event `json:"events,omitempty"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

type persistedSkillRevision struct {
	Revision      string    `json:"revision"`
	ResourceName  string    `json:"resource_name"`
	SHA256        string    `json:"sha256"`
	ArchiveBase64 string    `json:"archive_base64"`
	CreatedAt     time.Time `json:"created_at"`
}

type persistedSkillRecord struct {
	Name            string                   `json:"name"`
	DefaultRevision string                   `json:"default_revision"`
	Revisions       []persistedSkillRevision `json:"revisions,omitempty"`
}

func (s *Server) loadPersistedState() {
	path := strings.TrimSpace(s.cfg.StateFile)
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("sre-gateway: read broker state %s: %v", path, err)
		}
		return
	}
	var snap persistedBrokerState
	if err := json.Unmarshal(raw, &snap); err != nil {
		log.Printf("sre-gateway: decode broker state %s: %v", path, err)
		return
	}

	if s.agentEngine != nil {
		s.agentEngine.restorePersistedState(snap)
	}
	if s.skillRegistry != nil {
		s.skillRegistry.restorePersistedSkills(snap.Skills)
	}
	if len(snap.PendingGates) > 0 {
		s.mu.Lock()
		maps.Copy(s.pendingGates, snap.PendingGates)
		s.mu.Unlock()
	}
}

func (s *Server) savePersistedState() {
	if s.agentEngine == nil || s.skillRegistry == nil {
		return
	}
	path := strings.TrimSpace(s.cfg.StateFile)
	if path == "" {
		return
	}
	s.stateWriteMu.Lock()
	defer s.stateWriteMu.Unlock()

	snap := persistedBrokerState{Version: 1}
	if s.agentEngine != nil {
		s.agentEngine.snapshotPersistedState(&snap)
	}
	if s.skillRegistry != nil {
		snap.Skills = s.skillRegistry.snapshotPersistedSkills()
	}
	s.mu.Lock()
	if len(s.pendingGates) > 0 {
		snap.PendingGates = maps.Clone(s.pendingGates)
	}
	s.mu.Unlock()

	encoded, err := json.Marshal(snap)
	if err != nil {
		log.Printf("sre-gateway: marshal broker state: %v", err)
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("sre-gateway: mkdir broker state dir %s: %v", dir, err)
		return
	}
	tmp, err := os.CreateTemp(dir, ".broker-state-*.tmp")
	if err != nil {
		log.Printf("sre-gateway: create temp broker state in %s: %v", dir, err)
		return
	}
	tmpName := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		log.Printf("sre-gateway: write broker state %s: %v", tmpName, err)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		log.Printf("sre-gateway: commit broker state %s: %v", path, err)
	}
}

func (p *agentEngineProxy) snapshotPersistedState(out *persistedBrokerState) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.autoBootstrap && p.engineID != "" {
		out.DiscoveredReasoningEngineID = p.engineID
	}
	out.Sessions = make([]persistedSessionRecord, 0, len(p.sessionOrder))
	for _, key := range p.sessionOrder {
		rec, ok := p.sessions[key]
		if !ok {
			continue
		}
		out.Sessions = append(out.Sessions, persistedSessionRecord{
			AppName:        rec.appName,
			UserID:         rec.userID,
			SessionID:      rec.sessionID,
			CloudSessionID: rec.cloudSessionID,
			IncidentID:     rec.incidentID,
			Template:       rec.template,
			State:          maps.Clone(rec.state),
			Events:         slices.Clone(rec.events),
			UpdatedAt:      rec.updatedAt,
		})
	}
	if len(p.appState) > 0 {
		out.AppState = make(map[string]map[string]any, len(p.appState))
		for k, v := range p.appState {
			out.AppState[k] = maps.Clone(v)
		}
	}
	if len(p.userState) > 0 {
		out.UserState = make(map[string]map[string]map[string]any, len(p.userState))
		for appK, users := range p.userState {
			uCopy := make(map[string]map[string]any, len(users))
			for uK, st := range users {
				uCopy[uK] = maps.Clone(st)
			}
			out.UserState[appK] = uCopy
		}
	}
	if len(p.memoriesByApp) > 0 {
		out.MemoriesByApp = make(map[string][]ConsolidatedMemoryFact, len(p.memoriesByApp))
		for k, facts := range p.memoriesByApp {
			out.MemoriesByApp[k] = slices.Clone(facts)
		}
	}
}

func (p *agentEngineProxy) restorePersistedState(in persistedBrokerState) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.autoBootstrap && p.engineID == "" && strings.TrimSpace(in.DiscoveredReasoningEngineID) != "" {
		p.engineID = normalizeReasoningEngineID(in.DiscoveredReasoningEngineID)
	}
	for _, rec := range in.Sessions {
		if rec.AppName == "" || rec.SessionID == "" {
			continue
		}
		key := sessionMapKey(rec.AppName, rec.SessionID)
		if _, exists := p.sessions[key]; !exists {
			p.sessionOrder = append(p.sessionOrder, key)
		}
		p.sessions[key] = &storedLedgerSession{
			appName:        rec.AppName,
			userID:         rec.UserID,
			sessionID:      rec.SessionID,
			cloudSessionID: rec.CloudSessionID,
			incidentID:     rec.IncidentID,
			template:       rec.Template,
			state:          maps.Clone(rec.State),
			events:         slices.Clone(rec.Events),
			updatedAt:      rec.UpdatedAt,
		}
	}
	for k, v := range in.AppState {
		p.appState[k] = maps.Clone(v)
	}
	for appK, users := range in.UserState {
		if p.userState[appK] == nil {
			p.userState[appK] = make(map[string]map[string]any, len(users))
		}
		for uK, st := range users {
			p.userState[appK][uK] = maps.Clone(st)
		}
	}
	for k, facts := range in.MemoriesByApp {
		reindexed := make([]ConsolidatedMemoryFact, 0, len(facts))
		for _, f := range facts {
			reindexConsolidatedFact(&f)
			reindexed = append(reindexed, f)
		}
		p.memoriesByApp[k] = reindexed
	}
}

func reindexConsolidatedFact(f *ConsolidatedMemoryFact) {
	indexBlob := f.Fact + " " + f.SourceSession + " " + f.IncidentID + " " + strings.Join(f.Topics, " ")
	f.words = tokenizeMemoryWords(indexBlob)
	f.textLower = strings.ToLower(indexBlob)
}

func (p *skillRegistryProxy) snapshotPersistedSkills() []persistedSkillRecord {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]persistedSkillRecord, 0, len(p.skillOrder))
	for _, name := range p.skillOrder {
		rec, ok := p.skills[name]
		if !ok {
			continue
		}
		revs := make([]persistedSkillRevision, 0, len(rec.revisionOrder))
		for _, rName := range rec.revisionOrder {
			rv, ok := rec.revisions[rName]
			if !ok {
				continue
			}
			revs = append(revs, persistedSkillRevision{
				Revision:      rv.revision,
				ResourceName:  rv.resourceName,
				SHA256:        rv.sha256,
				ArchiveBase64: base64.StdEncoding.EncodeToString(rv.zipBytes),
				CreatedAt:     rv.createdAt,
			})
		}
		out = append(out, persistedSkillRecord{
			Name:            rec.name,
			DefaultRevision: rec.defaultRevision,
			Revisions:       revs,
		})
	}
	return out
}

func (p *skillRegistryProxy) restorePersistedSkills(records []persistedSkillRecord) {
	for _, sk := range records {
		if !skillIDPattern.MatchString(sk.Name) {
			continue
		}
		for _, rv := range sk.Revisions {
			zipBytes, err := base64.StdEncoding.DecodeString(rv.ArchiveBase64)
			if err != nil || len(zipBytes) == 0 {
				continue
			}
			setDef := rv.Revision == sk.DefaultRevision
			_, _ = p.upsertRevision(sk.Name, rv.Revision, rv.SHA256, zipBytes, setDef, rv.ResourceName, false)
		}
		if sk.DefaultRevision != "" {
			p.mu.Lock()
			if rec, ok := p.skills[sk.Name]; ok {
				if revObj, hasRev := rec.revisions[sk.DefaultRevision]; hasRev {
					rec.defaultRevision = sk.DefaultRevision
					rec.description = revObj.frontmatter.Description
				}
			}
			p.mu.Unlock()
		}
	}
}
