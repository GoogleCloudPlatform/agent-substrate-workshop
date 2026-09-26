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

package frontdoor

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agent-substrate/workshop/app/internal/metrics"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
)

type recorder struct {
	mu           sync.Mutex
	begun, ended []string
}

func (r *recorder) Begin(actor string) func() {
	r.mu.Lock()
	r.begun = append(r.begun, actor)
	r.mu.Unlock()
	return func() { r.mu.Lock(); r.ended = append(r.ended, actor); r.mu.Unlock() }
}

func setup(t *testing.T, router http.HandlerFunc) (*httptest.Server, *recorder, *metrics.Metrics) {
	t.Helper()
	up := httptest.NewServer(router)
	t.Cleanup(up.Close)
	rec, m := &recorder{}, metrics.New()
	fd := &FrontDoor{RouterURL: up.URL, Atespace: "workshop", Client: up.Client(), Activity: rec, Metrics: m}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/{actor}", fd.HandleChat)
	mux.HandleFunc("GET /actors/{actor}/state", fd.HandleState)
	mux.HandleFunc("GET /actors/{actor}/work", fd.HandleWork)
	gw := httptest.NewServer(mux)
	t.Cleanup(gw.Close)
	return gw, rec, m
}

func TestChatForwardsWithRouteHeader(t *testing.T) {
	var gotHeader, gotPath, gotBody string
	gw, rec, m := setup(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeader, gotPath = r.Header.Get(RouteHeader), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"reply":"hi","turn":1,"backend":"mock"}`)
	})
	resp, err := http.Post(gw.URL+"/chat/buddy", "application/json", strings.NewReader(`{"message":"yo"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"reply":"hi"`) {
		t.Fatalf("gateway = %d %s", resp.StatusCode, body)
	}
	if gotHeader != "workshop/buddy" || gotPath != "/chat" || gotBody != `{"message":"yo"}` {
		t.Fatalf("router saw header=%q path=%q body=%q", gotHeader, gotPath, gotBody)
	}
	if len(rec.begun) != 1 || len(rec.ended) != 1 || rec.begun[0] != "buddy" {
		t.Fatalf("activity begun=%v ended=%v", rec.begun, rec.ended)
	}
	if promtest.ToFloat64(m.ChatTurns.WithLabelValues("ok")) != 1 {
		t.Fatal("ok turn not counted")
	}
}

func TestFrontDoorPropagatesUpstreamStatus(t *testing.T) {
	gw, rec, m := setup(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no worker available", http.StatusServiceUnavailable)
	})
	resp, err := http.Post(gw.URL+"/chat/buddy", "application/json", strings.NewReader(`{"message":"yo"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("error body is not JSON: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(out["error"], "no worker available") {
		t.Fatalf("gateway = %d %v", resp.StatusCode, out)
	}
	if len(rec.ended) != 1 {
		t.Fatal("activity not ended on error")
	}
	if promtest.ToFloat64(m.ChatTurns.WithLabelValues("error")) != 1 {
		t.Fatal("error turn not counted")
	}
}

func TestJSONUpstreamErrorPassesThrough(t *testing.T) {
	gw, _, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"model call failed: 403 Forbidden"}`)
	})
	resp, _ := http.Post(gw.URL+"/chat/buddy", "application/json", strings.NewReader(`{"message":"yo"}`))
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 || strings.TrimSpace(string(b)) != `{"error":"model call failed: 403 Forbidden"}` {
		t.Fatalf("gateway = %d %s", resp.StatusCode, b)
	}
}

func TestRouterUnreachableIs502JSON(t *testing.T) {
	m := metrics.New()
	fd := &FrontDoor{RouterURL: "http://127.0.0.1:1", Atespace: "workshop", Client: http.DefaultClient, Activity: &recorder{}, Metrics: m}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/chat/buddy", strings.NewReader(`{"message":"x"}`))
	req.SetPathValue("actor", "buddy")
	fd.HandleChat(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("gateway = %d %s", rec.Code, rec.Body)
	}
}

func TestRejectsInvalidActorName(t *testing.T) {
	gw, _, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { t.Error("router must not be called") })
	for _, name := range []string{"Bad_Name", "-lead", strings.Repeat("a", 64)} {
		resp, _ := http.Post(gw.URL+"/chat/"+name, "application/json", strings.NewReader(`{"message":"x"}`))
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", name, resp.StatusCode)
		}
	}
}

func TestStateForwards(t *testing.T) {
	gw, _, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/state" || r.Header.Get(RouteHeader) != "workshop/memo" {
			t.Errorf("router saw %s %s", r.URL.Path, r.Header.Get(RouteHeader))
		}
		_, _ = io.WriteString(w, `{"memCount":3}`)
	})
	resp, _ := http.Get(gw.URL + "/actors/memo/state")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"memCount":3`) {
		t.Fatalf("state = %s", b)
	}
}

func TestWorkForwardsStepsAndCountsAsActivity(t *testing.T) {
	gw, rec, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/work" || r.URL.Query().Get("steps") != "150" || r.Header.Get(RouteHeader) != "workshop/swarm-03" {
			t.Errorf("router saw %s %s", r.URL, r.Header.Get(RouteHeader))
		}
		_, _ = io.WriteString(w, `{"steps":150}`)
	})
	resp, err := http.Get(gw.URL + "/actors/swarm-03/work?steps=150")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.begun) != 1 || rec.begun[0] != "swarm-03" || len(rec.ended) != 1 {
		t.Fatalf("activity begun=%v ended=%v", rec.begun, rec.ended)
	}
}
