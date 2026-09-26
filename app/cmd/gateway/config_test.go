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

package main

import (
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfigDefaults(t *testing.T) {
	c, err := loadConfig(env(map[string]string{"ATESPACE": "workshop"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":8080" || c.RouterURL != "http://atenet-router.ate-system.svc.cluster.local" ||
		c.ATE.Addr != "api.ate-system.svc.cluster.local:443" || c.ATE.TokenFile != "/var/run/ate/token/token" ||
		c.ATE.CAFile != "/var/run/ate/trust/trust-bundle.pem" || c.IdleAfter != 0 || c.IdleCheckEvery != 5*time.Second ||
		c.MockLLM || c.GeminiModel != "gemini-2.5-flash" {
		t.Fatalf("defaults = %+v", c)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	c, err := loadConfig(env(map[string]string{
		"ATESPACE": "a", "MOCK_LLM": "true", "IDLE_AFTER": "20s", "IDLE_CHECK_EVERY": "2s", "ATE_API_INSECURE": "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.MockLLM || c.IdleAfter != 20*time.Second || c.IdleCheckEvery != 2*time.Second || !c.ATE.Insecure {
		t.Fatalf("overrides = %+v", c)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	for name, m := range map[string]map[string]string{
		"no atespace":  {},
		"bad duration": {"ATESPACE": "a", "IDLE_AFTER": "twenty"},
		"bad bool":     {"ATESPACE": "a", "MOCK_LLM": "yes please"},
		"zero check":   {"ATESPACE": "a", "IDLE_CHECK_EVERY": "0s"},
	} {
		if _, err := loadConfig(env(m)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
