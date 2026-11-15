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

// Package netutil provides per-IP token-bucket rate limiting and RemoteAddr
// host extraction shared across sre-gateway, ops-hub, and ledger-api.
package netutil

import (
	"net"
	"sync"
	"time"
)

const maxTrackedClients = 4096

// ClientIP extracts the host portion of an HTTP Request.RemoteAddr value.
func ClientIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil || host == "" {
		return remoteAddr
	}
	return host
}

// RateLimiter enforces a per-key token-bucket rate limit bounded to maxTrackedClients entries.
type RateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	now     func() time.Time
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter constructs a RateLimiter replenishing ratePerSec tokens up to burst.
func NewRateLimiter(ratePerSec, burst float64) *RateLimiter {
	return NewRateLimiterWithClock(ratePerSec, burst, time.Now)
}

// NewRateLimiterWithClock constructs a RateLimiter using a caller-supplied clock.
func NewRateLimiterWithClock(ratePerSec, burst float64, clock func() time.Time) *RateLimiter {
	if clock == nil {
		clock = time.Now
	}
	return &RateLimiter{
		rate:    ratePerSec,
		burst:   burst,
		now:     clock,
		buckets: make(map[string]*bucket),
	}
}

// Allow consumes one token for key and reports whether the request is permitted.
func (l *RateLimiter) Allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buckets) > maxTrackedClients {
		clear(l.buckets)
	}
	b, ok := l.buckets[key]
	if !ok {
		l.buckets[key] = &bucket{tokens: l.burst - 1, last: now}
		return l.burst >= 1
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.rate
	}
	b.last = now
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
