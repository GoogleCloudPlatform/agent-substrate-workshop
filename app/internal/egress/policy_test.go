package egress

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"github.com/agent-substrate/workshop/app/internal/ateclient"
	"github.com/agent-substrate/workshop/app/internal/testutil"
)

const gatewayPolicy = `
# Allow only the workshop gateway.
rules:
- hostnames:
    patterns: ["gateway.workshop.svc.cluster.local"]
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
	if got := p.GetRules()[0].GetHostnames().GetPatterns(); len(got) != 1 || got[0] != "gateway.workshop.svc.cluster.local" {
		t.Fatalf("patterns = %v", got)
	}
}

func TestParsePolicyAllAndCIDR(t *testing.T) {
	p, err := ParsePolicy([]byte("rules:\n- cidrs:\n    cidrs: [\"10.0.0.0/8\"]\n- all: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.GetRules()[0].GetCidrs() == nil || p.GetRules()[1].GetAll() == nil {
		t.Fatalf("rules = %v", p.GetRules())
	}
}

func TestParsePolicyErrors(t *testing.T) {
	for name, in := range map[string]string{
		"empty":    "rules: []\n",
		"unknown":  "rules:\n- hostname: {patterns: [x]}\n",
		"metadata": "metadata: {name: x}\nrules:\n- all: {}\n",
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
	p2, _ := ParsePolicy([]byte("rules:\n- all: {}\n"))
	created, err := Apply(context.Background(), c, ref, p2)
	if err != nil || created {
		t.Fatalf("second apply = %v, %v", created, err)
	}
	if f.Policy("workshop", "buddy").GetRules()[0].GetAll() == nil {
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

// The policy files attendees apply in Module 4 must parse.
func TestWorkshopPolicyFilesParse(t *testing.T) {
	files, err := filepath.Glob("../../../modules/04-make-it-an-agent/manifests/egress-*.yaml")
	if err != nil || len(files) != 2 {
		t.Fatalf("glob = %v, %v; want 2 files", files, err)
	}
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
