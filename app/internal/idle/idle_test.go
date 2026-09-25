package idle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"github.com/agent-substrate/workshop/app/internal/ateclient"
	"github.com/agent-substrate/workshop/app/internal/frontdoor"
	"github.com/agent-substrate/workshop/app/internal/metrics"
	"github.com/agent-substrate/workshop/app/internal/testutil"
	promtest "github.com/prometheus/client_golang/prometheus/testutil"
)

// Tracker must satisfy frontdoor.Activity: the gateway front door records chat activity
// through this interface, and idle.Tracker is the concrete implementation used in
// production wiring (cmd/gateway). idle imports frontdoor here (not the reverse), since
// frontdoor has no reason to know about idle.
var _ frontdoor.Activity = (*Tracker)(nil)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

type fakeAPI struct {
	actors    []ActorInfo
	suspended []string
	err       error
	onSuspend func(ctx context.Context, name string) error // optional; runs before recording
}

func (f *fakeAPI) ListActors(context.Context, string) ([]ActorInfo, error) { return f.actors, nil }
func (f *fakeAPI) Suspend(ctx context.Context, _, name string) error {
	if f.onSuspend != nil {
		if err := f.onSuspend(ctx, name); err != nil {
			return err
		}
	}
	if f.err != nil {
		return f.err
	}
	f.suspended = append(f.suspended, name)
	return nil
}

func newSuspender(api API, c *clock, idleAfter time.Duration) (*Suspender, *metrics.Metrics) {
	m := metrics.New()
	return &Suspender{API: api, Atespace: "workshop", IdleAfter: idleAfter, Tracker: NewTracker(c.now), Metrics: m}, m
}

func TestSuspenderSuspendsOnlyIdleRunning(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	api := &fakeAPI{actors: []ActorInfo{{"chatty", true}, {"quiet", true}, {"asleep", false}}}
	s, m := newSuspender(api, c, 20*time.Second)

	s.Tracker.Touch("quiet")
	c.advance(15 * time.Second)
	s.Tracker.Touch("chatty")
	c.advance(10 * time.Second) // quiet idle 25s, chatty idle 10s
	if err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(api.suspended) != 1 || api.suspended[0] != "quiet" {
		t.Fatalf("suspended %v, want [quiet]", api.suspended)
	}
	// Tracked = all actors seen this tick (3). Awake = RUNNING actors after this tick's
	// suspends: chatty stays running (1) and quiet was just suspended, so awake = 1.
	if promtest.ToFloat64(m.ActorsTracked) != 3 || promtest.ToFloat64(m.ActorsAwake) != 1 {
		t.Fatalf("gauges tracked=%v awake=%v", promtest.ToFloat64(m.ActorsTracked), promtest.ToFloat64(m.ActorsAwake))
	}
	if promtest.ToFloat64(m.IdleSuspends.WithLabelValues("ok")) != 1 {
		t.Fatal("suspend not counted")
	}
}

func TestUnknownRunningActorGetsGracePeriod(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	api := &fakeAPI{actors: []ActorInfo{{"curl-woken", true}}}
	s, _ := newSuspender(api, c, 20*time.Second)
	_ = s.Tick(context.Background()) // first sighting starts its idle clock
	c.advance(19 * time.Second)
	_ = s.Tick(context.Background())
	if len(api.suspended) != 0 {
		t.Fatalf("suspended too early: %v", api.suspended)
	}
	c.advance(2 * time.Second)
	_ = s.Tick(context.Background())
	if len(api.suspended) != 1 {
		t.Fatalf("not suspended after grace: %v", api.suspended)
	}
}

func TestBusyActorIsNeverSuspended(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	api := &fakeAPI{actors: []ActorInfo{{"thinking", true}}}
	s, _ := newSuspender(api, c, 20*time.Second)
	done := s.Tracker.Begin("thinking") // a long model call in flight
	c.advance(time.Minute)
	_ = s.Tick(context.Background())
	if len(api.suspended) != 0 {
		t.Fatalf("suspended a busy actor: %v", api.suspended)
	}
	done()
	c.advance(21 * time.Second)
	_ = s.Tick(context.Background())
	if len(api.suspended) != 1 {
		t.Fatalf("idle clock did not start at end of request: %v", api.suspended)
	}
}

func TestDisabledSuspenderOnlyUpdatesGauges(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	api := &fakeAPI{actors: []ActorInfo{{"a", true}}}
	s, m := newSuspender(api, c, 0)
	c.advance(time.Hour)
	_ = s.Tick(context.Background())
	_ = s.Tick(context.Background())
	if len(api.suspended) != 0 || promtest.ToFloat64(m.ActorsAwake) != 1 {
		t.Fatalf("suspended=%v awake=%v", api.suspended, promtest.ToFloat64(m.ActorsAwake))
	}
}

func TestSuspendErrorIsCountedAndRetried(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	api := &fakeAPI{actors: []ActorInfo{{"a", true}}, err: errors.New("FailedPrecondition")}
	s, m := newSuspender(api, c, time.Second)
	_ = s.Tick(context.Background())
	c.advance(2 * time.Second)
	_ = s.Tick(context.Background())
	api.err = nil
	_ = s.Tick(context.Background())
	if promtest.ToFloat64(m.IdleSuspends.WithLabelValues("error")) != 1 || len(api.suspended) != 1 {
		t.Fatalf("errors=%v suspended=%v", promtest.ToFloat64(m.IdleSuspends.WithLabelValues("error")), api.suspended)
	}
}

func TestSuspendHasATimeout(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	var deadlines []time.Duration
	api := &fakeAPI{actors: []ActorInfo{{"hung", true}, {"next", true}}}
	api.onSuspend = func(ctx context.Context, name string) error {
		d, ok := ctx.Deadline()
		if !ok {
			return errors.New("no deadline")
		}
		deadlines = append(deadlines, time.Until(d))
		if name == "hung" {
			<-ctx.Done() // a SuspendActor that never answers
			return ctx.Err()
		}
		return nil
	}
	s, m := newSuspender(api, c, time.Second)
	s.SuspendTimeout = 50 * time.Millisecond
	_ = s.Tick(context.Background()) // first sighting
	c.advance(2 * time.Second)
	start := time.Now()
	_ = s.Tick(context.Background())
	if time.Since(start) > 5*time.Second {
		t.Fatalf("Tick blocked %v on a hung Suspend", time.Since(start))
	}
	if len(api.suspended) != 1 || api.suspended[0] != "next" {
		t.Fatalf("suspended %v, want [next] after the hung call timed out", api.suspended)
	}
	if promtest.ToFloat64(m.IdleSuspends.WithLabelValues("error")) != 1 {
		t.Fatal("timed-out suspend not counted as an error")
	}

	// The default is DefaultSuspendTimeout.
	deadlines = nil
	s2, _ := newSuspender(&fakeAPI{actors: []ActorInfo{{"a", true}}, onSuspend: api.onSuspend}, c, time.Second)
	_ = s2.Tick(context.Background())
	c.advance(2 * time.Second)
	_ = s2.Tick(context.Background())
	if len(deadlines) != 1 || deadlines[0] > DefaultSuspendTimeout || deadlines[0] < DefaultSuspendTimeout-5*time.Second {
		t.Fatalf("default suspend deadline = %v, want ~%v", deadlines, DefaultSuspendTimeout)
	}
}

func TestRequestDuringEarlierSuspendSparesLaterActor(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	api := &fakeAPI{actors: []ActorInfo{{"a", true}, {"b", true}}}
	s, _ := newSuspender(api, c, 20*time.Second)
	_ = s.Tick(context.Background()) // first sighting of both
	c.advance(30 * time.Second)
	// While a's (slow, synchronous) Suspend runs, a chat request for b starts.
	api.onSuspend = func(_ context.Context, name string) error {
		if name == "a" {
			s.Tracker.Begin("b")
		}
		return nil
	}
	_ = s.Tick(context.Background())
	if len(api.suspended) != 1 || api.suspended[0] != "a" {
		t.Fatalf("suspended %v, want [a]: b's idle state must be re-checked right before its Suspend", api.suspended)
	}
}

func TestGRPCAdapterPaginatesAndSuspends(t *testing.T) {
	f := &testutil.FakeControl{PageCap: 1, Actors: []*ateapipb.Actor{
		testutil.NewActor("workshop", "a", ateapipb.ActorState_ACTOR_STATE_RUNNING),
		testutil.NewActor("workshop", "b", ateapipb.ActorState_ACTOR_STATE_SUSPENDED),
		testutil.NewActor("workshop", "c", ateapipb.ActorState_ACTOR_STATE_RESUMING),
	}}
	addr, ca := testutil.StartFake(t, f)
	c, closer, err := ateclient.Dial(ateclient.Options{Addr: addr, CAFile: ca})
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	g := GRPC{C: c}
	got, err := g.ListActors(context.Background(), "workshop")
	if err != nil || len(got) != 3 || !got[0].Running || got[1].Running || got[2].Running {
		t.Fatalf("ListActors = %+v, %v", got, err)
	}
	if err := g.Suspend(context.Background(), "workshop", "a"); err != nil {
		t.Fatal(err)
	}
	if s := f.Suspended(); len(s) != 1 || s[0] != "workshop/a" {
		t.Fatalf("suspended %v", s)
	}
}
