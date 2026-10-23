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

package egress

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.ate.dev/substrate/pkg/proto/ateapipb"
	"github.com/agent-substrate/workshop/app/internal/ateclient"
	"github.com/agent-substrate/workshop/app/internal/testutil"
	"google.golang.org/protobuf/encoding/protojson"
	"sigs.k8s.io/yaml"
)

const gatewayPolicy = `
# Allow only the workshop gateway.
rules:
- http:
    hostnames: ["gateway.workshop.svc.cluster.local"]
`

func dial(t *testing.T, f *testutil.FakeControl) ateapipb.ControlClient {
	t.Helper()
	addr, ca := testutil.StartFake(t, f)
	c, closer, err := ateclient.Dial(ateclient.Options{Addr: addr, CAFile: ca})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closer.Close() })
	return c
}

func TestParsePolicyHostnames(t *testing.T) {
	p, err := ParsePolicy([]byte(gatewayPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.GetRules()[0].GetHttp().GetHostnames(); len(got) != 1 || got[0] != "gateway.workshop.svc.cluster.local" {
		t.Fatalf("hostnames = %v", got)
	}
}

func TestParsePolicyProtocols(t *testing.T) {
	p, err := ParsePolicy([]byte("rules:\n- http:\n    hostnames: [\"*\"]\n    ports:\n      all: {}\n- https:\n    hostnames: [\"*\"]\n- tlsPassthrough:\n    hostnames: [\"*.example.com\"]\n    ports:\n      numbers: [443]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.GetRules()[0].GetHttp().GetPorts().GetAll() == nil || p.GetRules()[1].GetHttps() == nil || p.GetRules()[2].GetTlsPassthrough() == nil {
		t.Fatalf("rules = %v", p.GetRules())
	}
}

func TestParsePolicyErrors(t *testing.T) {
	for name, in := range map[string]string{
		"empty":    "rules: []\n",
		"unknown":  "rules:\n- hostname: {patterns: [x]}\n",
		"metadata": "metadata: {name: x}\nrules:\n- http: {hostnames: [\"*\"]}\n",
		"not yaml": ":\n  - [",
	} {
		if _, err := ParsePolicy([]byte(in)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestApplyCreatesWithMetadata(t *testing.T) {
	f := &testutil.FakeControl{}
	c := dial(t, f)
	ref := &ateapipb.ObjectRef{Atespace: "workshop", Name: "buddy"}
	p, _ := ParsePolicy([]byte(gatewayPolicy))
	created, err := Apply(context.Background(), c, ref, p)
	if err != nil || !created {
		t.Fatalf("first apply = %v, %v", created, err)
	}
	if got := f.Policy("workshop", "buddy"); got.GetMetadata().GetName() != PolicyName || got.GetMetadata().GetAtespace() != "workshop" {
		t.Fatalf("stored metadata = %v", got.GetMetadata())
	}
}

func TestApplyUpdatesExistingPolicy(t *testing.T) {
	f := &testutil.FakeControl{}
	c := dial(t, f)
	ref := &ateapipb.ObjectRef{Atespace: "workshop", Name: "buddy"}
	p1, _ := ParsePolicy([]byte(gatewayPolicy))
	if _, err := Apply(context.Background(), c, ref, p1); err != nil {
		t.Fatal(err)
	}
	p2, _ := ParsePolicy([]byte("rules:\n- https:\n    hostnames: [\"*\"]\n"))
	created, err := Apply(context.Background(), c, ref, p2)
	if err != nil || created {
		t.Fatalf("second apply = %v, %v", created, err)
	}
	if f.Policy("workshop", "buddy").GetRules()[0].GetHttps() == nil {
		t.Fatal("policy not updated")
	}
}

func TestTargetsAllPaginates(t *testing.T) {
	f := &testutil.FakeControl{PageCap: 2}
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		f.Actors = append(f.Actors, testutil.NewActor("workshop", n, ateapipb.ActorState_ACTOR_STATE_SUSPENDED))
	}
	f.Actors = append(f.Actors, testutil.NewActor("other", "z", ateapipb.ActorState_ACTOR_STATE_SUSPENDED))
	refs, err := Targets(context.Background(), dial(t, f), "workshop", "", true)
	if err != nil || len(refs) != 5 {
		t.Fatalf("Targets = %v, %v", refs, err)
	}
}

func TestTargetsRequiresExactlyOneSelector(t *testing.T) {
	c := dial(t, &testutil.FakeControl{})
	if _, err := Targets(context.Background(), c, "workshop", "", false); err == nil || !strings.Contains(err.Error(), "--actor") {
		t.Fatalf("neither: %v", err)
	}
	if _, err := Targets(context.Background(), c, "workshop", "x", true); err == nil {
		t.Fatal("both: want error")
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	c := dial(t, &testutil.FakeControl{})
	if err := Delete(context.Background(), c, &ateapipb.ObjectRef{Atespace: "workshop", Name: "nobody"}); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
}

// The policy files attendees apply in Module 4 and optional labs B and J must parse.
func TestWorkshopPolicyFilesParse(t *testing.T) {
	files, err := filepath.Glob("../../../modules/04-make-it-an-agent/manifests/egress-*.yaml")
	if err != nil || len(files) != 2 {
		t.Fatalf("glob = %v, %v; want 2 files", files, err)
	}
	files = append(files,
		"../../../optional/B-ax/egress-github.yaml",
		"../../../optional/J-credential-injection/egress-fetch.yaml",
	)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParsePolicy(b); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

// The policy file optional lab A applies to the ate-env atespace must parse too.
func TestOptionalLabAPolicyFileParses(t *testing.T) {
	const f = "../../../optional/A-env/egress-pypi.yaml"
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePolicy(b)
	if err != nil {
		t.Fatalf("%s: %v", f, err)
	}
	if got := p.GetRules()[0].GetHttps().GetHostnames(); len(got) != 2 || got[0] != "pypi.org" || got[1] != "files.pythonhosted.org" {
		t.Fatalf("hostnames = %v", got)
	}
}

// Every ActorTemplate manifest in modules/ and optional/ must unmarshal cleanly into ateapipb.ActorTemplate.
func TestWorkshopActorTemplateFilesParse(t *testing.T) {
	modFiles, err := filepath.Glob("../../../modules/*/manifests/template.yaml.tmpl")
	if err != nil || len(modFiles) != 3 {
		t.Fatalf("modules glob = %v, %v; want 3 files", modFiles, err)
	}
	optFiles, err := filepath.Glob("../../../optional/*/template.yaml.tmpl")
	if err != nil || len(optFiles) != 3 {
		t.Fatalf("optional glob = %v, %v; want 3 files", optFiles, err)
	}
	replacer := strings.NewReplacer(
		"${ATESPACE}", "workshop",
		"${AGENT_IMAGE}", "us-central1-docker.pkg.dev/p/workshop/agent@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"${FETCH_IMAGE}", "us-central1-docker.pkg.dev/p/workshop/agent-fetch@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"${BUCKET_NAME}", "p-ate-snapshots",
	)
	for _, f := range append(modFiles, optFiles...) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		rendered := replacer.Replace(string(b))
		j, err := yaml.YAMLToJSON([]byte(rendered))
		if err != nil {
			t.Fatalf("%s: YAMLToJSON: %v", f, err)
		}
		tmpl := &ateapipb.ActorTemplate{}
		if err := protojson.Unmarshal(j, tmpl); err != nil {
			t.Errorf("%s: protojson.Unmarshal ActorTemplate: %v", f, err)
		}
	}
}
