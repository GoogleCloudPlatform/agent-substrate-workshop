// Package model serves the gateway's /v1/model endpoint. Actors never hold model
// credentials: they POST their conversation here, and the gateway calls Vertex AI with its
// own Workload Identity, or answers from a mock.
package model

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/agent-substrate/workshop/app/internal/chatapi"
	"github.com/agent-substrate/workshop/app/internal/metrics"
)

// Result is one model reply with token usage.
type Result struct {
	Reply        string
	InputTokens  int
	OutputTokens int
}

// Backend generates a reply to a conversation whose last message is from the user.
type Backend interface {
	Name() string
	Generate(ctx context.Context, msgs []chatapi.Message) (Result, error)
}

// Mock answers without any model. Fallback marks a mock chosen because Vertex failed to init.
type Mock struct{ Fallback bool }

func (m Mock) Name() string {
	if m.Fallback {
		return "mock-fallback"
	}
	return "mock"
}

func (Mock) Generate(_ context.Context, msgs []chatapi.Message) (Result, error) {
	turn := 0
	for _, m := range msgs {
		if m.Role == "user" {
			turn++
		}
	}
	last := msgs[len(msgs)-1].Content
	return Result{Reply: fmt.Sprintf("mock model (turn %d): you said %q", turn, last), InputTokens: len(last) / 4, OutputTokens: 8}, nil
}

// Select picks the backend: the mock when asked, otherwise Vertex, falling back to the mock
// if Vertex cannot be initialized so the workshop keeps moving.
func Select(mock bool, newVertex func() (Backend, error)) Backend {
	if mock {
		return Mock{}
	}
	b, err := newVertex()
	if err != nil {
		log.Printf("vertex unavailable (%v); using mock-fallback", err)
		return Mock{Fallback: true}
	}
	return b
}

// Handler serves POST /v1/model.
func Handler(b Backend, m *metrics.Metrics) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, chatapi.Error{Error: "POST only"})
			return
		}
		var req chatapi.ModelRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil ||
			len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Role != "user" {
			writeJSON(w, http.StatusBadRequest, chatapi.Error{Error: `body must be {"messages":[...]} ending with a "user" message`})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		start := time.Now()
		res, err := b.Generate(ctx, req.Messages)
		if err != nil {
			m.ObserveModel(b.Name(), "error", time.Since(start), 0, 0)
			log.Printf("model %s: %v", b.Name(), err)
			writeJSON(w, http.StatusBadGateway, chatapi.Error{Error: fmt.Sprintf("%s: %v", b.Name(), err)})
			return
		}
		m.ObserveModel(b.Name(), "ok", time.Since(start), res.InputTokens, res.OutputTokens)
		writeJSON(w, http.StatusOK, chatapi.ModelResponse{
			Reply: res.Reply, Backend: b.Name(), InputTokens: res.InputTokens, OutputTokens: res.OutputTokens,
		})
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
