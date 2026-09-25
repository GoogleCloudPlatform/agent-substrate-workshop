// Package idle suspends actors nobody is talking to. Substrate keeps a RUNNING actor on its
// worker until someone suspends it, so a platform has to decide what "idle" means. Here:
// no request through the gateway front door for IdleAfter.
package idle

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"github.com/agent-substrate/workshop/app/internal/metrics"
)

// Tracker records the last time each actor finished a request, and which ones are mid-request.
type Tracker struct {
	mu       sync.Mutex
	now      func() time.Time
	last     map[string]time.Time
	inflight map[string]int
}

func NewTracker(now func() time.Time) *Tracker {
	if now == nil {
		now = time.Now
	}
	return &Tracker{now: now, last: map[string]time.Time{}, inflight: map[string]int{}}
}

// Begin marks a request to actor as started. The returned func marks it done and restarts
// the actor's idle clock.
func (t *Tracker) Begin(actor string) func() {
	t.mu.Lock()
	t.inflight[actor]++
	t.last[actor] = t.now()
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			t.inflight[actor]--
			t.last[actor] = t.now()
			t.mu.Unlock()
		})
	}
}

// Touch restarts actor's idle clock.
func (t *Tracker) Touch(actor string) {
	t.mu.Lock()
	t.last[actor] = t.now()
	t.mu.Unlock()
}

// idleFor reports how long actor has been idle. An actor seen for the first time (woken by
// something other than the gateway) starts its clock now.
func (t *Tracker) idleFor(actor string) (time.Duration, bool) {
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

// Forget drops an actor so its next wake-up gets a fresh grace period.
func (t *Tracker) Forget(actor string) {
	t.mu.Lock()
	delete(t.last, actor)
	t.mu.Unlock()
}

// ActorInfo is the slice of actor state the suspender needs.
type ActorInfo struct {
	Name    string
	Running bool
}

// API is the part of ateapi the suspender uses.
type API interface {
	ListActors(ctx context.Context, atespace string) ([]ActorInfo, error)
	Suspend(ctx context.Context, atespace, name string) error
}

// GRPC adapts an ateapi Control client to API.
type GRPC struct{ C ateapipb.ControlClient }

func (g GRPC) ListActors(ctx context.Context, atespace string) ([]ActorInfo, error) {
	var out []ActorInfo
	token := ""
	for {
		resp, err := g.C.ListActors(ctx, &ateapipb.ListActorsRequest{Atespace: atespace, PageSize: 100, PageToken: token})
		if err != nil {
			return nil, err
		}
		for _, a := range resp.GetActors() {
			out = append(out, ActorInfo{
				Name:    a.GetMetadata().GetName(),
				Running: a.GetStatus().GetState() == ateapipb.ActorState_ACTOR_STATE_RUNNING,
			})
		}
		if token = resp.GetNextPageToken(); token == "" {
			return out, nil
		}
	}
}

func (g GRPC) Suspend(ctx context.Context, atespace, name string) error {
	_, err := g.C.SuspendActor(ctx, &ateapipb.SuspendActorRequest{Actor: &ateapipb.ObjectRef{Atespace: atespace, Name: name}})
	return err
}

// Suspender suspends RUNNING actors idle for at least IdleAfter. IdleAfter <= 0 disables
// suspending but still updates the fleet gauges.
type Suspender struct {
	API       API
	Atespace  string
	IdleAfter time.Duration
	Tracker   *Tracker
	Metrics   *metrics.Metrics
}

// Tick runs one check.
func (s *Suspender) Tick(ctx context.Context) error {
	actors, err := s.API.ListActors(ctx, s.Atespace)
	if err != nil {
		return err
	}
	awake := 0
	for _, a := range actors {
		if !a.Running {
			s.Tracker.Forget(a.Name)
			continue
		}
		awake++
		if s.IdleAfter <= 0 {
			continue
		}
		idle, busy := s.Tracker.idleFor(a.Name)
		if busy || idle < s.IdleAfter {
			continue
		}
		if err := s.API.Suspend(ctx, s.Atespace, a.Name); err != nil {
			s.Metrics.IdleSuspends.WithLabelValues("error").Inc()
			log.Printf("idle-suspend %s/%s: %v", s.Atespace, a.Name, err)
			continue
		}
		s.Metrics.IdleSuspends.WithLabelValues("ok").Inc()
		s.Tracker.Forget(a.Name)
		awake--
		log.Printf("idle-suspend %s/%s after %s idle", s.Atespace, a.Name, idle.Round(time.Second))
	}
	s.Metrics.ActorsTracked.Set(float64(len(actors)))
	s.Metrics.ActorsAwake.Set(float64(awake))
	return nil
}

// Run calls Tick every interval until ctx is done.
func (s *Suspender) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil {
			log.Printf("idle check: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
