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

// Package model serves the gateway's /v1/model endpoint. Actors never hold model
// credentials: they POST their conversation here, and the gateway calls Gemini on the
// Gemini platform with its own Workload Identity, or answers from a mock.
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
	// Backend names who actually answered when that isn't the Backend's own Name(), i.e. a
	// per-request fallback. UpstreamErr is the error that caused the fallback.
	Backend     string
	UpstreamErr error
}

// Backend generates a reply to a conversation whose last message is from the user.
type Backend interface {
	Name() string
	Generate(ctx context.Context, msgs []chatapi.Message) (Result, error)
}

// Mock answers without any model. Fallback marks a mock answering because Gemini failed,
// either at startup or on this request.
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

// Select picks the backend: the mock when asked, otherwise Gemini. It falls back to the mock
// if Gemini can't be initialized, and per request if a Gemini call fails, so the workshop
// keeps moving and the reply's backend says "mock-fallback" instead of looking like an egress 502.
func Select(mock bool, newGemini func() (Backend, error)) Backend {
	if mock {
		return Mock{}
	}
	b, err := newGemini()
	if err != nil {
		log.Printf("gemini unavailable (%v); using mock-fallback", err)
		return Mock{Fallback: true}
	}
	return WithFallback(b)
}

// WithFallback answers a request from Mock{Fallback: true} when b fails it, and logs b's error.
func WithFallback(b Backend) Backend { return fallback{b} }

type fallback struct{ primary Backend }

func (f fallback) Name() string { return f.primary.Name() }

func (f fallback) Generate(ctx context.Context, msgs []chatapi.Message) (Result, error) {
	res, err := f.primary.Generate(ctx, msgs)
	if err == nil {
		return res, nil
	}
	mock := Mock{Fallback: true}
	log.Printf("model %s failed (%v); answering this request from %s", f.primary.Name(), err, mock.Name())
	res, mockErr := mock.Generate(ctx, msgs)
	if mockErr != nil {
		return Result{}, err
	}
	res.Backend, res.UpstreamErr = mock.Name(), err
	return res, nil
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
		name, upstream := b.Name(), ""
		if res.UpstreamErr != nil {
			// The primary failed and a fallback answered: count the failure against the primary too.
			m.ObserveModel(b.Name(), "error", time.Since(start), 0, 0)
			upstream = fmt.Sprintf("%s: %v", b.Name(), res.UpstreamErr)
		}
		if res.Backend != "" {
			name = res.Backend
		}
		m.ObserveModel(name, "ok", time.Since(start), res.InputTokens, res.OutputTokens)
		writeJSON(w, http.StatusOK, chatapi.ModelResponse{
			Reply: res.Reply, Backend: name, InputTokens: res.InputTokens, OutputTokens: res.OutputTokens,
			UpstreamError: upstream,
		})
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
