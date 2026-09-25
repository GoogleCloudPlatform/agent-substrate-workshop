package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-substrate/workshop/app/internal/chatapi"
	"github.com/agent-substrate/workshop/app/internal/metrics"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/genai"
)

type failing struct{}

func (failing) Name() string { return "vertex" }
func (failing) Generate(context.Context, []chatapi.Message) (Result, error) {
	return Result{}, errors.New("permission denied on aiplatform.endpoints.predict")
}

func post(t *testing.T, h http.Handler, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/model", strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestMockCountsUserTurns(t *testing.T) {
	r, err := Mock{}.Generate(context.Background(), []chatapi.Message{
		{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "hello"},
	})
	if err != nil || r.Reply != `mock model (turn 2): you said "hello"` {
		t.Fatalf("mock = %q, %v", r.Reply, err)
	}
}

func TestSelect(t *testing.T) {
	called := false
	vertex := func() (Backend, error) { called = true; return failing{}, nil }
	if b := Select(true, vertex); b.Name() != "mock" || called {
		t.Errorf("mock=true: got %s, vertex called=%v", b.Name(), called)
	}
	if b := Select(false, vertex); b.Name() != "vertex" {
		t.Errorf("mock=false: got %s", b.Name())
	}
	broken := func() (Backend, error) { return nil, errors.New("no credentials") }
	if b := Select(false, broken); b.Name() != "mock-fallback" {
		t.Errorf("vertex init failure: got %s", b.Name())
	}
}

func TestHandlerOK(t *testing.T) {
	m := metrics.New()
	code, out := post(t, Handler(Mock{}, m), `{"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || out["backend"] != "mock" || !strings.Contains(out["reply"].(string), `"hi"`) {
		t.Fatalf("handler = %d %v", code, out)
	}
	if n := promtest.CollectAndCount(m.ModelDuration); n != 1 {
		t.Errorf("duration series = %d", n)
	}
}

func TestHandlerRejectsBadRequests(t *testing.T) {
	h := Handler(Mock{}, metrics.New())
	for _, body := range []string{`nope`, `{"messages":[]}`, `{"messages":[{"role":"assistant","content":"x"}]}`} {
		if code, _ := post(t, h, body); code != http.StatusBadRequest {
			t.Errorf("body %s: status %d, want 400", body, code)
		}
	}
}

func TestHandlerBackendErrorIs502(t *testing.T) {
	m := metrics.New()
	code, out := post(t, Handler(failing{}, m), `{"messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusBadGateway || !strings.Contains(out["error"].(string), "permission denied") {
		t.Fatalf("handler = %d %v", code, out)
	}
	if n := promtest.CollectAndCount(m.ModelDuration); n != 1 {
		t.Errorf("want one (vertex,error) duration series, got %d", n)
	}
}

type answering struct{}

func (answering) Name() string { return "vertex" }
func (answering) Generate(context.Context, []chatapi.Message) (Result, error) {
	return Result{Reply: "from gemini", InputTokens: 3, OutputTokens: 4}, nil
}

func TestVertexErrorFallsBackToMockPerRequest(t *testing.T) {
	m := metrics.New()
	b := Select(false, func() (Backend, error) { return failing{}, nil })
	code, out := post(t, Handler(b, m), `{"messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK || out["backend"] != "mock-fallback" || !strings.Contains(out["reply"].(string), `"hi"`) {
		t.Fatalf("handler = %d %v", code, out)
	}
	if up, _ := out["upstreamError"].(string); !strings.Contains(up, "vertex: permission denied") {
		t.Errorf("upstreamError = %q", up)
	}
	// One (vertex,error) and one (mock-fallback,ok) duration series.
	if n := promtest.CollectAndCount(m.ModelDuration); n != 2 {
		t.Errorf("duration series = %d, want 2", n)
	}
}

func TestVertexSuccessStaysOnVertex(t *testing.T) {
	m := metrics.New()
	b := Select(false, func() (Backend, error) { return answering{}, nil })
	code, out := post(t, Handler(b, m), `{"messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK || out["backend"] != "vertex" || out["reply"] != "from gemini" {
		t.Fatalf("handler = %d %v", code, out)
	}
	if _, ok := out["upstreamError"]; ok {
		t.Errorf("upstreamError set on success: %v", out)
	}
	if n := promtest.CollectAndCount(m.ModelDuration); n != 1 {
		t.Errorf("duration series = %d, want 1", n)
	}
}

func TestToContentsMapsRoles(t *testing.T) {
	got := toContents([]chatapi.Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}})
	if len(got) != 2 || got[0].Role != genai.RoleUser || got[1].Role != genai.RoleModel || got[1].Parts[0].Text != "a" {
		t.Fatalf("contents = %+v", got)
	}
}
