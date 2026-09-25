package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"github.com/agent-substrate/workshop/app/internal/testutil"
)

func TestApplyAllThenGet(t *testing.T) {
	f := &testutil.FakeControl{Actors: []*ateapipb.Actor{
		testutil.NewActor("workshop", "a", ateapipb.ActorState_ACTOR_STATE_SUSPENDED),
		testutil.NewActor("workshop", "b", ateapipb.ActorState_ACTOR_STATE_RUNNING),
	}}
	addr, ca := testutil.StartFake(t, f)
	policy := filepath.Join(t.TempDir(), "p.yaml")
	_ = os.WriteFile(policy, []byte("rules:\n- all: {}\n"), 0o600)
	common := []string{"-a", "workshop", "--addr", addr, "--ca-file", ca}

	var out bytes.Buffer
	if err := run(context.Background(), append(common, "apply", "-f", policy, "--all"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "created workshop/a") || !strings.Contains(out.String(), "created workshop/b") {
		t.Fatalf("apply output = %q", out.String())
	}
	out.Reset()
	if err := run(context.Background(), append(common, "get", "--actor", "a"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "all: {}") {
		t.Fatalf("get output = %q", out.String())
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"bogus"}, {"apply", "--all"}, {"get"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
			t.Errorf("args %v: want error", args)
		}
	}
}
