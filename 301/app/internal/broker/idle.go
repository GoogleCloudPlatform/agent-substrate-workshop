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

package broker

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/301/app/internal/ateclient"
	ateapipb "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/grpc"
)

// DefaultSuspendTimeout bounds a single SuspendActor RPC so one slow checkpoint
// cannot stall subsequent idle checks.
const DefaultSuspendTimeout = 60 * time.Second

// IdleTracker records in-flight requests and last-completed timestamps per actor.
type IdleTracker struct {
	mu       sync.Mutex
	now      func() time.Time
	last     map[string]time.Time
	inflight map[string]int
}

// NewIdleTracker constructs an IdleTracker using clock (defaults to time.Now).
func NewIdleTracker(clock func() time.Time) *IdleTracker {
	if clock == nil {
		clock = time.Now
	}
	return &IdleTracker{
		now:      clock,
		last:     make(map[string]time.Time),
		inflight: make(map[string]int),
	}
}

// Begin marks an active turn on actor and returns a completion callback that
// decrements the in-flight count and resets the idle timer.
func (t *IdleTracker) Begin(actor string) func() {
	t.mu.Lock()
	t.inflight[actor]++
	t.last[actor] = t.now()
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			t.inflight[actor]--
			if t.inflight[actor] < 0 {
				t.inflight[actor] = 0
			}
			t.last[actor] = t.now()
			t.mu.Unlock()
		})
	}
}

// Touch resets actor's idle clock without holding a turn open.
func (t *IdleTracker) Touch(actor string) {
	t.mu.Lock()
	t.last[actor] = t.now()
	t.mu.Unlock()
}

// Forget removes actor from the idle map so its next wakeup gets a full window.
func (t *IdleTracker) Forget(actor string) {
	t.mu.Lock()
	delete(t.last, actor)
	t.mu.Unlock()
}

// IdleStatus returns how long actor has been idle and whether a turn is in flight.
func (t *IdleTracker) IdleStatus(actor string) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.inflight[actor] > 0 {
		return 0, true
	}
	last, ok := t.last[actor]
	if !ok {
		t.last[actor] = t.now()
		return 0, false
	}
	return t.now().Sub(last), false
}

// ObserveIdleSeconds returns elapsed idle seconds without seeding new entries.
func (t *IdleTracker) ObserveIdleSeconds(actor string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.inflight[actor] > 0 {
		return 0
	}
	last, ok := t.last[actor]
	if !ok {
		return 0
	}
	sec := int64(t.now().Sub(last).Seconds())
	if sec < 0 {
		return 0
	}
	return sec
}

// suspendClient is the narrow ateapi surface used by IdleSuspender.
type suspendClient interface {
	ListActors(ctx context.Context, in *ateapipb.ListActorsRequest, opts ...grpc.CallOption) (*ateapipb.ListActorsResponse, error)
	SuspendActor(ctx context.Context, in *ateapipb.SuspendActorRequest, opts ...grpc.CallOption) (*ateapipb.SuspendActorResponse, error)
}

// IdleSuspender transitions RUNNING actors in Atespace that have been idle for
// at least IdleAfter to ACTOR_STATE_SUSPENDED in Cloud Storage (0 vCPU).
type IdleSuspender struct {
	Control        suspendClient
	Atespace       string
	IdleAfter      time.Duration
	SuspendTimeout time.Duration
	Tracker        *IdleTracker
	OnSuspend      func(actorName string, reason string)
}

// Tick inspects current actors once and suspends any whose idle time >= IdleAfter.
func (s *IdleSuspender) Tick(ctx context.Context) error {
	if s.Control == nil || s.Atespace == "" {
		return nil
	}
	actors, err := ateclient.ListAllActors(ctx, s.Control, s.Atespace)
	if err != nil {
		return err
	}
	for _, a := range actors {
		name := a.GetMetadata().GetName()
		if a.GetStatus().GetState() != ateapipb.ActorState_ACTOR_STATE_RUNNING {
			s.Tracker.Forget(name)
			continue
		}
		if s.IdleAfter <= 0 {
			continue
		}
		idleDur, busy := s.Tracker.IdleStatus(name)
		if busy || idleDur < s.IdleAfter {
			continue
		}
		timeout := s.SuspendTimeout
		if timeout <= 0 {
			timeout = DefaultSuspendTimeout
		}
		sctx, cancel := context.WithTimeout(ctx, timeout)
		err := ateclient.SuspendActor(sctx, s.Control, s.Atespace, name)
		cancel()
		if err != nil {
			log.Printf("sre-gateway: idle-suspend %s/%s failed: %v", s.Atespace, name, err)
			continue
		}
		s.Tracker.Forget(name)
		if s.OnSuspend != nil {
			s.OnSuspend(name, "idle timeout ("+idleDur.Round(time.Millisecond).String()+") -> SUSPENDED (0 vCPU)")
		}
	}
	return nil
}

// Run executes Tick on every interval until ctx is canceled.
func (s *IdleSuspender) Run(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 2 * time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
				log.Printf("sre-gateway: idle check error: %v", err)
			}
		}
	}
}
