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
	m.ObserveModel("vertex", "ok", 1500*time.Millisecond, 12, 34)
	if got := promtest.ToFloat64(m.ModelTokens.WithLabelValues("vertex", "input")); got != 12 {
		t.Errorf("input tokens = %v", got)
	}
	if got := promtest.ToFloat64(m.ModelTokens.WithLabelValues("vertex", "output")); got != 34 {
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
