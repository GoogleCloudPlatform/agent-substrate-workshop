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

package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	promtest "github.com/prometheus/client_golang/prometheus/testutil"
)

func TestObserveModelRecordsDurationAndTokens(t *testing.T) {
	m := New()
	m.ObserveModel("gemini", "ok", 1500*time.Millisecond, 12, 34)
	if got := promtest.ToFloat64(m.ModelTokens.WithLabelValues("gemini", "input")); got != 12 {
		t.Errorf("input tokens = %v", got)
	}
	if got := promtest.ToFloat64(m.ModelTokens.WithLabelValues("gemini", "output")); got != 34 {
		t.Errorf("output tokens = %v", got)
	}
	if n := promtest.CollectAndCount(m.ModelDuration); n != 1 {
		t.Errorf("duration series = %d", n)
	}
}

func TestHandlerExposesWorkshopAndRuntimeMetrics(t *testing.T) {
	m := New()
	m.ChatTurns.WithLabelValues("ok").Inc()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	for _, want := range []string{
		`workshop_chat_turns_total{outcome="ok"} 1`,
		"workshop_actors_awake 0",
		"go_goroutines",
		"process_cpu_seconds_total",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics output missing %q", want)
		}
	}
}
