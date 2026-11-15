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

package ateclient_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ateapipb "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"sigs.k8s.io/yaml"
)

func TestModule01ProtoJSONManifestSchemas(t *testing.T) {
	manifestsDir := filepath.Join("..", "..", "..", "modules", "01-actor-boundary", "manifests")
	replacer := strings.NewReplacer(
		"${ATESPACE}", "workshop-301",
		"${NAMESPACE}", "workshop-301",
		"${AGENT_IMAGE}", "us-central1-docker.pkg.dev/demo-proj/workshop/agent@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"${GEMINI_MODEL}", "gemini-3.8-flash",
	)

	tmplRaw, err := os.ReadFile(filepath.Join(manifestsDir, "template-v1.yaml.tmpl"))
	if err != nil {
		t.Fatalf("read template-v1.yaml.tmpl: %v", err)
	}
	tmplJSON, err := yaml.YAMLToJSON([]byte(replacer.Replace(string(tmplRaw))))
	if err != nil {
		t.Fatalf("YAMLToJSON template-v1: %v", err)
	}
	var actorTmpl ateapipb.ActorTemplate
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(tmplJSON, &actorTmpl); err != nil {
		t.Fatalf("protojson.Unmarshal template-v1 into ActorTemplate failed: %v", err)
	}
	if got := actorTmpl.GetMetadata().GetName(); got != "sre-agent-v1" {
		t.Errorf("ActorTemplate name = %q, want sre-agent-v1", got)
	}
	if got := actorTmpl.GetSnapshotConfig().GetPreferredFidelity(); got != ateapipb.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY {
		t.Errorf("PreferredFidelity = %v, want SNAPSHOT_FIDELITY_MEMORY", got)
	}
	if len(actorTmpl.GetContainers()) != 1 || actorTmpl.GetContainers()[0].GetWakeupProbe().GetHttpGet().GetPort() != 80 {
		t.Errorf("WakeupProbe port = %v, want 80", actorTmpl.GetContainers())
	}

	egRaw, err := os.ReadFile(filepath.Join(manifestsDir, "egress-policy.yaml.tmpl"))
	if err != nil {
		t.Fatalf("read egress-policy.yaml.tmpl: %v", err)
	}
	egJSON, err := yaml.YAMLToJSON([]byte(replacer.Replace(string(egRaw))))
	if err != nil {
		t.Fatalf("YAMLToJSON egress-policy: %v", err)
	}
	var egressPol ateapipb.EgressPolicy
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(egJSON, &egressPol); err != nil {
		t.Fatalf("protojson.Unmarshal egress-policy into EgressPolicy failed: %v", err)
	}
	if len(egressPol.GetRules()) != 1 {
		t.Fatalf("EgressPolicy rules count = %d, want 1", len(egressPol.GetRules()))
	}
	hosts := egressPol.GetRules()[0].GetHttp().GetHostnames()
	if len(hosts) != 1 || hosts[0] != "sre-gateway.workshop-301.svc.cluster.local" {
		t.Errorf("EgressPolicy hostnames = %v, want [sre-gateway.workshop-301.svc.cluster.local]", hosts)
	}
}

func TestModule02ProtoJSONManifestSchemas(t *testing.T) {
	manifestsDir := filepath.Join("..", "..", "..", "modules", "02-sessions-and-memory", "manifests")
	replacer := strings.NewReplacer(
		"${ATESPACE}", "workshop-301",
		"${NAMESPACE}", "workshop-301",
		"${AGENT_IMAGE}", "us-central1-docker.pkg.dev/demo-proj/workshop/agent@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		"${GEMINI_MODEL}", "gemini-3.8-flash",
	)

	tmplRaw, err := os.ReadFile(filepath.Join(manifestsDir, "template-v2.yaml.tmpl"))
	if err != nil {
		t.Fatalf("read template-v2.yaml.tmpl: %v", err)
	}
	tmplJSON, err := yaml.YAMLToJSON([]byte(replacer.Replace(string(tmplRaw))))
	if err != nil {
		t.Fatalf("YAMLToJSON template-v2: %v", err)
	}
	var actorTmpl ateapipb.ActorTemplate
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(tmplJSON, &actorTmpl); err != nil {
		t.Fatalf("protojson.Unmarshal template-v2 into ActorTemplate failed: %v", err)
	}
	if got := actorTmpl.GetMetadata().GetName(); got != "sre-agent-v2" {
		t.Errorf("ActorTemplate name = %q, want sre-agent-v2", got)
	}
	if got := actorTmpl.GetSnapshotConfig().GetPreferredFidelity(); got != ateapipb.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY {
		t.Errorf("PreferredFidelity = %v, want SNAPSHOT_FIDELITY_MEMORY", got)
	}
	if len(actorTmpl.GetContainers()) != 1 || actorTmpl.GetContainers()[0].GetWakeupProbe().GetHttpGet().GetPort() != 80 {
		t.Fatalf("WakeupProbe port = %v, want 80", actorTmpl.GetContainers())
	}
	var sawTemplateEnv bool
	for _, kv := range actorTmpl.GetContainers()[0].GetEnv() {
		if kv.GetName() == "ACTOR_TEMPLATE" && kv.GetValue() == "sre-agent-v2" {
			sawTemplateEnv = true
		}
		if strings.Contains(strings.ToUpper(kv.GetName()), "KEY") || strings.Contains(strings.ToUpper(kv.GetName()), "TOKEN") {
			t.Errorf("keyless ActorTemplate sre-agent-v2 contains unexpected credential env %q", kv.GetName())
		}
	}
	if !sawTemplateEnv {
		t.Errorf("template-v2.yaml.tmpl missing ACTOR_TEMPLATE=sre-agent-v2 env")
	}
}

func TestModule03ProtoJSONManifestSchemas(t *testing.T) {
	manifestsDir := filepath.Join("..", "..", "..", "modules", "03-skill-registry", "manifests")
	replacer := strings.NewReplacer(
		"${ATESPACE}", "workshop-301",
		"${NAMESPACE}", "workshop-301",
		"${AGENT_IMAGE}", "us-central1-docker.pkg.dev/demo-proj/workshop/agent@sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
		"${GEMINI_MODEL}", "gemini-3.8-flash",
	)

	tmplRaw, err := os.ReadFile(filepath.Join(manifestsDir, "template-v3.yaml.tmpl"))
	if err != nil {
		t.Fatalf("read template-v3.yaml.tmpl: %v", err)
	}
	tmplJSON, err := yaml.YAMLToJSON([]byte(replacer.Replace(string(tmplRaw))))
	if err != nil {
		t.Fatalf("YAMLToJSON template-v3: %v", err)
	}
	var actorTmpl ateapipb.ActorTemplate
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(tmplJSON, &actorTmpl); err != nil {
		t.Fatalf("protojson.Unmarshal template-v3 into ActorTemplate failed: %v", err)
	}
	if got := actorTmpl.GetMetadata().GetName(); got != "sre-agent-v3" {
		t.Errorf("ActorTemplate name = %q, want sre-agent-v3", got)
	}
	if got := actorTmpl.GetSnapshotConfig().GetPreferredFidelity(); got != ateapipb.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY {
		t.Errorf("PreferredFidelity = %v, want SNAPSHOT_FIDELITY_MEMORY", got)
	}
	if len(actorTmpl.GetContainers()) != 1 || actorTmpl.GetContainers()[0].GetWakeupProbe().GetHttpGet().GetPort() != 80 {
		t.Fatalf("WakeupProbe port = %v, want 80", actorTmpl.GetContainers())
	}
	var sawTemplateEnv bool
	for _, kv := range actorTmpl.GetContainers()[0].GetEnv() {
		if kv.GetName() == "ACTOR_TEMPLATE" && kv.GetValue() == "sre-agent-v3" {
			sawTemplateEnv = true
		}
		if strings.Contains(strings.ToUpper(kv.GetName()), "KEY") || strings.Contains(strings.ToUpper(kv.GetName()), "TOKEN") {
			t.Errorf("keyless ActorTemplate sre-agent-v3 contains unexpected credential env %q", kv.GetName())
		}
	}
	if !sawTemplateEnv {
		t.Errorf("template-v3.yaml.tmpl missing ACTOR_TEMPLATE=sre-agent-v3 env")
	}
}

func TestModule04ProtoJSONManifestSchemas(t *testing.T) {
	manifestsDir := filepath.Join("..", "..", "..", "modules", "04-agent-registry-mcp-a2a", "manifests")
	replacer := strings.NewReplacer(
		"${ATESPACE}", "workshop-301",
		"${NAMESPACE}", "workshop-301",
		"${PROJECT_ID}", "demo-proj",
		"${REGION}", "us-central1",
		"${AGENT_IMAGE}", "us-central1-docker.pkg.dev/demo-proj/workshop/agent@sha256:11223344556677889900aabbccddeeff11223344556677889900aabbccddeeff",
		"${GEMINI_MODEL}", "gemini-3.8-flash",
	)

	tmplRaw, err := os.ReadFile(filepath.Join(manifestsDir, "template-v4.yaml.tmpl"))
	if err != nil {
		t.Fatalf("read template-v4.yaml.tmpl: %v", err)
	}
	tmplJSON, err := yaml.YAMLToJSON([]byte(replacer.Replace(string(tmplRaw))))
	if err != nil {
		t.Fatalf("YAMLToJSON template-v4: %v", err)
	}
	var actorTmpl ateapipb.ActorTemplate
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(tmplJSON, &actorTmpl); err != nil {
		t.Fatalf("protojson.Unmarshal template-v4 into ActorTemplate failed: %v", err)
	}
	if got := actorTmpl.GetMetadata().GetName(); got != "sre-agent-v4" {
		t.Errorf("ActorTemplate name = %q, want sre-agent-v4", got)
	}
	if got := actorTmpl.GetSnapshotConfig().GetPreferredFidelity(); got != ateapipb.SnapshotFidelity_SNAPSHOT_FIDELITY_MEMORY {
		t.Errorf("PreferredFidelity = %v, want SNAPSHOT_FIDELITY_MEMORY", got)
	}
	if len(actorTmpl.GetContainers()) != 1 || actorTmpl.GetContainers()[0].GetWakeupProbe().GetHttpGet().GetPort() != 80 {
		t.Fatalf("WakeupProbe port = %v, want 80", actorTmpl.GetContainers())
	}
	var sawTemplateEnv bool
	for _, kv := range actorTmpl.GetContainers()[0].GetEnv() {
		if kv.GetName() == "ACTOR_TEMPLATE" && kv.GetValue() == "sre-agent-v4" {
			sawTemplateEnv = true
		}
		if strings.Contains(strings.ToUpper(kv.GetName()), "KEY") || strings.Contains(strings.ToUpper(kv.GetName()), "TOKEN") {
			t.Errorf("keyless ActorTemplate sre-agent-v4 contains unexpected credential env %q", kv.GetName())
		}
	}
	if !sawTemplateEnv {
		t.Errorf("template-v4.yaml.tmpl missing ACTOR_TEMPLATE=sre-agent-v4 env")
	}
}
