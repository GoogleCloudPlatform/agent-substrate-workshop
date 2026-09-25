// Package frontdoor is the gateway's public chat API. It turns /chat/{actor} into a request
// to atenet-router carrying the ate-target-actor header. The router resumes the actor if it
// is suspended and parks the request until it is ready. Each call is also recorded as
// activity, so the idle-suspender knows which actors are in use.
package frontdoor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/agent-substrate/workshop/app/internal/chatapi"
	"github.com/agent-substrate/workshop/app/internal/metrics"
)

// RouteHeader tells atenet-router which actor a request is for, as "<atespace>/<actor>".
const RouteHeader = "ate-target-actor"

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// Activity is told when a request to an actor starts; the returned func marks it finished.
type Activity interface {
	Begin(actor string) (done func())
}

type FrontDoor struct {
	RouterURL string
	Atespace  string
	Client    *http.Client
	Activity  Activity
	Metrics   *metrics.Metrics
}

// HandleChat serves POST /chat/{actor}.
func (f *FrontDoor) HandleChat(w http.ResponseWriter, r *http.Request) {
	outcome := "error"
	defer func() { f.Metrics.ChatTurns.WithLabelValues(outcome).Inc() }()
	actor := r.PathValue("actor")
	if !dnsLabel.MatchString(actor) {
		writeJSON(w, http.StatusBadRequest, chatapi.Error{Error: fmt.Sprintf("invalid actor name %q", actor)})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, chatapi.Error{Error: err.Error()})
		return
	}
	if f.forward(w, r.Context(), http.MethodPost, actor, "/chat", body) {
		outcome = "ok"
	}
}

// HandleState serves GET /actors/{actor}/state.
func (f *FrontDoor) HandleState(w http.ResponseWriter, r *http.Request) {
	actor := r.PathValue("actor")
	if !dnsLabel.MatchString(actor) {
		writeJSON(w, http.StatusBadRequest, chatapi.Error{Error: fmt.Sprintf("invalid actor name %q", actor)})
		return
	}
	f.forward(w, r.Context(), http.MethodGet, actor, "/state", nil)
}

// forward proxies one request to the actor through the router and reports whether it got a 2xx.
func (f *FrontDoor) forward(w http.ResponseWriter, ctx context.Context, method, actor, path string, body []byte) bool {
	done := f.Activity.Begin(actor)
	defer done()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(f.RouterURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, chatapi.Error{Error: err.Error()})
		return false
	}
	req.Header.Set(RouteHeader, f.Atespace+"/"+actor)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, chatapi.Error{Error: "router: " + err.Error()})
		return false
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	if !ok && !json.Valid(b) {
		// The router answers errors in plain text; keep the status, make the body JSON.
		writeJSON(w, resp.StatusCode, chatapi.Error{Error: fmt.Sprintf("router: %s: %s", resp.Status, strings.TrimSpace(string(b)))})
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(b)
	return ok
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
