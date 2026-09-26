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

	"github.com/agent-substrate/workshop/app/internal/idle"
	"github.com/agent-substrate/workshop/app/internal/metrics"
)

// TestBuildComponentsShareOneTracker pins the wiring invariant the idle-suspender depends
// on: the front door and the suspender must observe the same activity, or a chat that keeps
// an actor busy would not stop the suspender from suspending it mid-turn.
func TestBuildComponentsShareOneTracker(t *testing.T) {
	cfg := config{RouterURL: "http://router.invalid", Atespace: "workshop", IdleAfter: 20 * time.Second}
	fd, suspender := buildComponents(cfg, nil, metrics.New())

	if fd.Client == nil {
		t.Fatal("front door Client is nil")
	}
	if fd.Client.Timeout != 0 {
		t.Fatalf("front door Client has a fixed timeout of %s; want none (or >= the router's 5m route-timeout)", fd.Client.Timeout)
	}
	if fd.Activity == nil {
		t.Fatal("front door Activity is nil")
	}
	if fd.Metrics == nil {
		t.Fatal("front door Metrics is nil")
	}
	if suspender.Tracker == nil {
		t.Fatal("suspender Tracker is nil")
	}

	activity, ok := fd.Activity.(*idle.Tracker)
	if !ok {
		t.Fatalf("front door Activity is %T, want *idle.Tracker", fd.Activity)
	}
	if activity != suspender.Tracker {
		t.Fatal("front door Activity and suspender Tracker are different *idle.Tracker instances")
	}
}
