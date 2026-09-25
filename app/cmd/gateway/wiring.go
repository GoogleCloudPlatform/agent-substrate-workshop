package main

import (
	"net/http"

	"github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"github.com/agent-substrate/workshop/app/internal/frontdoor"
	"github.com/agent-substrate/workshop/app/internal/idle"
	"github.com/agent-substrate/workshop/app/internal/metrics"
)

// buildComponents wires the front door and the idle-suspender around one shared
// *idle.Tracker: every chat the front door proxies restarts the same idle clock the
// suspender reads to decide when to suspend an actor. Returning both from one constructor
// keeps that invariant something a test can check, instead of something two separate call
// sites in main must independently get right.
func buildComponents(cfg config, ate ateapipb.ControlClient, m *metrics.Metrics) (*frontdoor.FrontDoor, *idle.Suspender) {
	tracker := idle.NewTracker(nil)
	fd := &frontdoor.FrontDoor{
		RouterURL: cfg.RouterURL,
		Atespace:  cfg.Atespace,
		// No fixed timeout: atenet-router already bounds each call with its own
		// parked-request budget (60s) and route-timeout (5m default). A shorter client
		// timeout here would cut off a legitimate resume-and-answer turn that the router
		// itself would have completed.
		Client:   &http.Client{},
		Activity: tracker,
		Metrics:  m,
	}
	suspender := &idle.Suspender{API: idle.GRPC{C: ate}, Atespace: cfg.Atespace, IdleAfter: cfg.IdleAfter, Tracker: tracker, Metrics: m}
	return fd, suspender
}
