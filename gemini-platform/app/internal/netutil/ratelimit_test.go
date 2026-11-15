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

package netutil_test

import (
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/agent-substrate-workshop/gemini-platform/app/internal/netutil"
)

func TestClientIPAndRateLimiter(t *testing.T) {
	if got := netutil.ClientIP("127.0.0.1:54321"); got != "127.0.0.1" {
		t.Errorf("ClientIP(127.0.0.1:54321) = %q, want 127.0.0.1", got)
	}
	if got := netutil.ClientIP("[::1]:8080"); got != "::1" {
		t.Errorf("ClientIP([::1]:8080) = %q, want ::1", got)
	}
	if got := netutil.ClientIP("unix-socket"); got != "unix-socket" {
		t.Errorf("ClientIP(unix-socket) = %q, want unix-socket", got)
	}

	cur := time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC)
	lim := netutil.NewRateLimiterWithClock(2, 2, func() time.Time { return cur })
	if !lim.Allow("127.0.0.1") || !lim.Allow("127.0.0.1") {
		t.Fatalf("expected initial burst of 2 tokens to succeed")
	}
	if lim.Allow("127.0.0.1") {
		t.Fatalf("expected 3rd immediate call to be rate-limited")
	}
	cur = cur.Add(600 * time.Millisecond)
	if !lim.Allow("127.0.0.1") {
		t.Fatalf("expected token replenishment after 600ms at 2 req/s")
	}
}
