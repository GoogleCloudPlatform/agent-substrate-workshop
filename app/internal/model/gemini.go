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

package model

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/agent-substrate/workshop/app/internal/chatapi"
	"google.golang.org/genai"
)

const systemPrompt = "You are a concise, friendly assistant running as an Agent Substrate actor in a workshop. Answer in at most three sentences."

type gemini struct {
	client *genai.Client
	model  string
}

// NewGemini returns a Gemini backend on the Gemini Enterprise Agent Platform. The Google GenAI SDK
// reads GOOGLE_GENAI_USE_ENTERPRISE and GOOGLE_CLOUD_LOCATION (default global) from the environment,
// which the gateway Deployment sets. Credentials come from Application Default Credentials, which on
// GKE is the pod's Workload Identity.
func NewGemini(ctx context.Context, project, model string) (Backend, error) {
	if project == "" || model == "" {
		return nil, errors.New("GOOGLE_CLOUD_PROJECT and GEMINI_MODEL must be set")
	}
	if v := strings.ToLower(os.Getenv("GOOGLE_GENAI_USE_ENTERPRISE")); v != "true" && v != "1" {
		return nil, errors.New("set GOOGLE_GENAI_USE_ENTERPRISE=true (the gateway Deployment does)")
	}
	c, err := genai.NewClient(ctx, &genai.ClientConfig{Project: project})
	if err != nil {
		return nil, err
	}
	return &gemini{client: c, model: model}, nil
}

func (*gemini) Name() string { return "gemini" }

func (g *gemini) Generate(ctx context.Context, msgs []chatapi.Message) (Result, error) {
	resp, err := g.client.Models.GenerateContent(ctx, g.model, toContents(msgs), &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(systemPrompt, genai.RoleUser),
	})
	if err != nil {
		return Result{}, err
	}
	r := Result{Reply: resp.Text()}
	if u := resp.UsageMetadata; u != nil {
		r.InputTokens, r.OutputTokens = int(u.PromptTokenCount), int(u.CandidatesTokenCount)
	}
	if r.Reply == "" {
		return Result{}, errors.New("empty response (blocked or no candidates)")
	}
	return r, nil
}

// toContents maps chat roles onto Gemini roles ("assistant" is "model" in Gemini).
func toContents(msgs []chatapi.Message) []*genai.Content {
	out := make([]*genai.Content, 0, len(msgs))
	for _, m := range msgs {
		role := genai.Role(genai.RoleUser)
		if m.Role == "assistant" {
			role = genai.RoleModel
		}
		out = append(out, genai.NewContentFromText(m.Content, role))
	}
	return out
}
