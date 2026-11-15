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
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-substrate/workshop/app/internal/chatapi"
)

// Background jobs outlive their request, so the knobs are set once here instead of per test.
func TestMain(m *testing.M) {
	hashesPerStep = 20_000 // a few ms per step
	heartbeatEvery = 5 * time.Millisecond
	os.Exit(m.Run())
}

func TestWorkReportsSteps(t *testing.T) {
	ts := newTestServer(t, Config{})
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"?steps=5", 5},
		{"", 60},         // default
		{"?steps=0", 60}, // out of range falls back
		{"?steps=601", 60},
		{"?steps=abc", 60},
	} {
		code, body := get(t, ts.URL+"/work"+tc.query)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", tc.query, code, body)
		}
		var res WorkResult
		if err := json.Unmarshal([]byte(body), &res); err != nil {
			t.Fatalf("%s: decode %q: %v", tc.query, body, err)
		}
		if res.Steps != tc.want || res.BootID == "" || res.Finished.Before(res.Started) {
			t.Errorf("%s: got %+v, want %d steps with a bootID", tc.query, res, tc.want)
		}
	}
}

// busyServer counts POSTs, the way the gateway's /actors/{actor}/busy would receive them.
func busyServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			n.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(ts.Close)
	return ts, &n
}

func postJob(t *testing.T, url, busyURL string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, nil)
	if busyURL != "" {
		req.Header.Set(chatapi.BusyURLHeader, busyURL)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d, want 202", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestJobHeartbeatsWhileRunningThenStops(t *testing.T) {
	busy, n := busyServer(t)
	ts := newTestServer(t, Config{Heartbeat: true})
	if got := postJob(t, ts.URL+"/jobs?steps=40", busy.URL); got["heartbeat"] != true {
		t.Fatalf("response %v, want heartbeat true", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for n.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n.Load() < 2 {
		t.Fatalf("got %d heartbeats while the job ran, want at least 2", n.Load())
	}
	// Once the job is done the heartbeats stop: the count settles.
	for prev := int32(-1); prev != n.Load(); {
		prev = n.Load()
		time.Sleep(50 * heartbeatEvery)
	}
}

func TestJobWithoutHeartbeatNeverReports(t *testing.T) {
	busy, n := busyServer(t)
	off := newTestServer(t, Config{})               // Heartbeat off: ignore the Busy-URL
	on := newTestServer(t, Config{Heartbeat: true}) // no Busy-URL (straight to the router): nothing to call
	if got := postJob(t, off.URL+"/jobs?steps=10", busy.URL); got["heartbeat"] != false {
		t.Fatalf("heartbeat off: response %v", got)
	}
	if got := postJob(t, on.URL+"/jobs?steps=10", ""); got["heartbeat"] != false {
		t.Fatalf("no Busy-URL: response %v", got)
	}
	time.Sleep(100 * heartbeatEvery)
	if n.Load() != 0 {
		t.Fatalf("got %d heartbeats, want 0", n.Load())
	}
}
