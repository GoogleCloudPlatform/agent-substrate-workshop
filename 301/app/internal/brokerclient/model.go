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

// Package brokerclient connects a keyless Incident Actor to the Governed Broker
// across the Actor Boundary without ambient cloud credentials.
package brokerclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/telemetry"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/genai"
)

// DefaultModelName is the default Gemini model on Gemini Enterprise Agent Platform.
const DefaultModelName = "gemini-3.8-flash"

// NewModel constructs an ADK Go v2 model.LLM that sends Gemini requests over HTTP
// to the Governed Broker's /genai endpoint. The Incident Actor carries zero API
// keys or bearer tokens; the Governed Broker attaches Workload Identity outside
// the sandbox.
func NewModel(ctx context.Context, brokerURL, modelName string, hc *http.Client) (model.LLM, error) {
	base := strings.TrimRight(strings.TrimSpace(brokerURL), "/")
	if base == "" {
		return nil, errors.New("brokerclient: BROKER_URL is required")
	}
	name := strings.TrimSpace(modelName)
	if name == "" {
		name = DefaultModelName
	}
	hc = telemetry.WrapHTTPClient(hc, 90*time.Second)
	llm, err := gemini.NewModel(ctx, name, &genai.ClientConfig{
		Backend: genai.BackendEnterprise,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: base + "/genai",
		},
		HTTPClient: hc,
	})
	if err != nil {
		return nil, fmt.Errorf("brokerclient: init Gemini model %s via %s/genai: %w", name, base, err)
	}
	return llm, nil
}
