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
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gke-demos/substrate-agent-platform/app/internal/ateclient"
	"github.com/gke-demos/substrate-agent-platform/app/internal/ateclient/atetest"
	ateapipb "go.ate.dev/substrate/api/ateapi/v1"
)

func TestEnsureActorWaitsForAsyncDelete(t *testing.T) {
	fake := atetest.NewFakeControl()
	addr, caFile, tokenFile := atetest.StartServer(t, fake)

	cli, closer, err := ateclient.Dial(ateclient.Options{
		Addr:      addr,
		CAFile:    caFile,
		TokenFile: tokenFile,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Seed an initial sre-agent-v3 actor.
	initial, err := ateclient.EnsureActor(ctx, cli, "workshop-301", "inc-402", "sre-agent-v3", "sre-gateway.workshop-301.svc.cluster.local")
	if err != nil {
		t.Fatalf("EnsureActor(v3): %v", err)
	}
	if got := initial.GetActorTemplate().GetName(); got != "sre-agent-v3" {
		t.Fatalf("initial template = %q, want sre-agent-v3", got)
	}

	// Case 1: External `kubectl ate delete actor inc-402` leaves the actor in ACTOR_STATE_DELETING.
	// EnsureActor must wait for deletion to finish rather than returning the dying actor.
	fake.MarkDeleting("workshop-301", "inc-402", 2)
	recreated, err := ateclient.EnsureActor(ctx, cli, "workshop-301", "inc-402", "sre-agent-v3", "sre-gateway.workshop-301.svc.cluster.local")
	if err != nil {
		t.Fatalf("EnsureActor after external delete: %v", err)
	}
	if got := recreated.GetStatus().GetState(); got == ateapipb.ActorState_ACTOR_STATE_DELETING {
		t.Fatalf("EnsureActor returned ACTOR_STATE_DELETING actor instead of waiting for teardown")
	}
	if fake.Policy("workshop-301", "inc-402") == nil {
		t.Fatalf("expected default-deny EgressPolicy attached on recreated actor")
	}

	// Case 2: Template upgrade (sre-agent-v3 -> sre-agent-v4) triggers asynchronous DeleteActor.
	fake.SetDeletePollCount(2)
	upgraded, err := ateclient.EnsureActor(ctx, cli, "workshop-301", "inc-402", "sre-agent-v4", "sre-gateway.workshop-301.svc.cluster.local")
	if err != nil {
		t.Fatalf("EnsureActor template upgrade v3 -> v4: %v", err)
	}
	if got := upgraded.GetActorTemplate().GetName(); got != "sre-agent-v4" {
		t.Fatalf("upgraded template = %q, want sre-agent-v4", got)
	}
	if got := upgraded.GetStatus().GetState(); got == ateapipb.ActorState_ACTOR_STATE_DELETING {
		t.Fatalf("upgraded actor remained in ACTOR_STATE_DELETING")
	}

	// Case 3: Context deadline is honored if an actor remains stuck in ACTOR_STATE_DELETING.
	fake.MarkDeleting("workshop-301", "inc-402", 1000)
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer shortCancel()
	if _, err := ateclient.EnsureActor(shortCtx, cli, "workshop-301", "inc-402", "sre-agent-v4", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("EnsureActor on stuck deleting actor err = %v, want DeadlineExceeded", err)
	}
}
