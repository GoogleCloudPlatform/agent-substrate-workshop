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

package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-substrate/workshop/app/internal/chatapi"
)

func newTestServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func getState(t *testing.T, base string) State {
	t.Helper()
	_, body := get(t, base+"/state")
	var st State
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatalf("decode state %q: %v", body, err)
	}
	return st
}

func chat(t *testing.T, base, msg string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(base+"/chat", "application/json", strings.NewReader(`{"message":`+jsonString(msg)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestReadyz(t *testing.T) {
	ts := newTestServer(t, Config{Greeting: "hi"})
	if code, body := get(t, ts.URL+"/readyz"); code != 200 || body != "ok\n" {
		t.Fatalf("readyz = %d %q", code, body)
	}
}

func TestRootCountsInMemoryOnlyWithoutDataDir(t *testing.T) {
	ts := newTestServer(t, Config{Greeting: "hi"})
	get(t, ts.URL+"/")
	_, body := get(t, ts.URL+"/")
	if !strings.HasPrefix(body, "hi (mem=2 disk=-1 boot=") {
		t.Fatalf("root = %q", body)
	}
	if st := getState(t, ts.URL); st.MemCount != 2 || st.DiskCount != -1 {
		t.Fatalf("state = %+v", st)
	}
}

func TestDiskCountSurvivesRestartButMemoryDoesNot(t *testing.T) {
	dir := t.TempDir()
	first := newTestServer(t, Config{Greeting: "hi", DataDir: dir})
	get(t, first.URL+"/")
	get(t, first.URL+"/")
	bootA := getState(t, first.URL).BootID

	second := newTestServer(t, Config{Greeting: "hi", DataDir: dir})
	_, body := get(t, second.URL+"/")
	if !strings.HasPrefix(body, "hi (mem=1 disk=3 boot=") {
		t.Fatalf("after restart root = %q", body)
	}
	if st := getState(t, second.URL); st.BootID == bootA {
		t.Fatalf("new process kept boot ID %q", bootA)
	}
}

func TestCorruptDiskCountIsReportedNotReset(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, countFile)
	if err := os.WriteFile(p, []byte("not-a-number"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := newTestServer(t, Config{Greeting: "hi", DataDir: dir})
	if code, body := get(t, ts.URL+"/"); code != http.StatusInternalServerError || !strings.Contains(body, "corrupt") {
		t.Fatalf("root = %d %q, want 500 naming the corrupt counter", code, body)
	}
	if b, _ := os.ReadFile(p); string(b) != "not-a-number" {
		t.Fatalf("counter overwritten with %q", b)
	}
	if st := getState(t, ts.URL); !strings.Contains(st.DiskError, "corrupt") {
		t.Fatalf("state = %+v, want diskError", st)
	}
}

func TestChatEchoMode(t *testing.T) {
	ts := newTestServer(t, Config{Greeting: "hi"})
	code, out := chat(t, ts.URL, "ping")
	if code != 200 || out["reply"] != "echo: ping" || out["backend"] != "echo" || out["turn"] != float64(1) {
		t.Fatalf("chat = %d %v", code, out)
	}
}

func TestChatRejectsEmptyMessage(t *testing.T) {
	ts := newTestServer(t, Config{Greeting: "hi"})
	for _, body := range []string{`{"message":"   "}`, `not json`, `{}`} {
		resp, err := http.Post(ts.URL+"/chat", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q: status %d, want 400", body, resp.StatusCode)
		}
	}
}

func TestChatSendsHistoryToModel(t *testing.T) {
	var got []chatapi.ModelRequest
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatapi.ModelRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		got = append(got, req)
		_ = json.NewEncoder(w).Encode(chatapi.ModelResponse{Reply: "pong", Backend: "mock"})
	}))
	defer model.Close()
	ts := newTestServer(t, Config{Greeting: "hi", ModelURL: model.URL})
	chat(t, ts.URL, "one")
	code, out := chat(t, ts.URL, "two")
	if code != 200 || out["reply"] != "pong" || out["backend"] != "mock" || out["turn"] != float64(2) {
		t.Fatalf("chat = %d %v", code, out)
	}
	last := got[len(got)-1].Messages
	want := []chatapi.Message{{Role: "user", Content: "one"}, {Role: "assistant", Content: "pong"}, {Role: "user", Content: "two"}}
	if len(last) != len(want) {
		t.Fatalf("model saw %v", last)
	}
	for i := range want {
		if last[i] != want[i] {
			t.Fatalf("model saw %v, want %v", last, want)
		}
	}
}

func TestChatModelFailureReturns502AndKeepsHistoryClean(t *testing.T) {
	fail := true
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "egress denied", http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(chatapi.ModelResponse{Reply: "ok", Backend: "mock"})
	}))
	defer model.Close()
	ts := newTestServer(t, Config{Greeting: "hi", ModelURL: model.URL})
	code, out := chat(t, ts.URL, "lost")
	if code != http.StatusBadGateway || !strings.Contains(out["error"].(string), "403") {
		t.Fatalf("chat = %d %v", code, out)
	}
	fail = false
	if _, out := chat(t, ts.URL, "kept"); out["turn"] != float64(1) {
		t.Fatalf("failed turn leaked into history: %v", out)
	}
}

func TestChatUnreachableModelReturns502(t *testing.T) {
	ts := newTestServer(t, Config{Greeting: "hi", ModelURL: "http://127.0.0.1:1/v1/model"})
	if code, out := chat(t, ts.URL, "x"); code != http.StatusBadGateway || out["error"] == nil {
		t.Fatalf("chat = %d %v", code, out)
	}
}

func TestHistorySurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	a := newTestServer(t, Config{Greeting: "hi", DataDir: dir})
	chat(t, a.URL, "remember me")
	b := newTestServer(t, Config{Greeting: "hi", DataDir: dir})
	if st := getState(t, b.URL); st.Turns != 1 {
		t.Fatalf("turns after restart = %d, want 1", st.Turns)
	}
}

func TestCorruptHistoryIsQuarantined(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, historyFile), []byte(`[{"role":"user","con`), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := newTestServer(t, Config{Greeting: "hi", DataDir: dir})
	if code, out := chat(t, ts.URL, "still alive"); code != 200 || out["turn"] != float64(1) {
		t.Fatalf("chat = %d %v", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, historyFile+".corrupt")); err != nil {
		t.Fatalf("corrupt file not quarantined: %v", err)
	}
}
