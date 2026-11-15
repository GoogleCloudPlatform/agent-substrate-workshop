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

// Package telemetry implements W3C traceparent propagation, OpenTelemetry GenAI
// span collection, secret/payload scrubbing, OTLP JSON ingestion, Cloud Trace v2
// export, and low-cardinality Prometheus metrics across the Actor Boundary.
package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	traceparentHeader = "traceparent"
	tracestateHeader  = "tracestate"
	zeroTraceID       = "00000000000000000000000000000000"
	zeroSpanID        = "0000000000000000"
)

var fallbackSeq atomic.Uint64

// SpanContext holds W3C Trace Context identifiers for a single trace span.
type SpanContext struct {
	TraceID    string
	SpanID     string
	Flags      string
	TraceState string
}

type spanCtxKey struct{}

// NewTraceID generates a 16-byte lowercase hex W3C trace identifier.
func NewTraceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil || isAllZero(b[:]) {
		binary.BigEndian.PutUint64(b[0:8], uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint64(b[8:16], fallbackSeq.Add(1))
	}
	return hex.EncodeToString(b[:])
}

// NewSpanID generates an 8-byte lowercase hex W3C span identifier.
func NewSpanID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil || isAllZero(b[:]) {
		binary.BigEndian.PutUint64(b[:], fallbackSeq.Add(1)^uint64(time.Now().UnixNano()))
	}
	return hex.EncodeToString(b[:])
}

func isAllZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// Valid reports whether sc contains well-formed non-zero W3C trace and span IDs.
func (sc SpanContext) Valid() bool {
	return isLowerHex(sc.TraceID, 32) && sc.TraceID != zeroTraceID &&
		isLowerHex(sc.SpanID, 16) && sc.SpanID != zeroSpanID
}

// Traceparent formats sc as a version-00 W3C traceparent header value.
func (sc SpanContext) Traceparent() string {
	if !sc.Valid() {
		return ""
	}
	flags := sc.Flags
	if !isLowerHex(flags, 2) {
		flags = "01"
	}
	return fmt.Sprintf("00-%s-%s-%s", sc.TraceID, sc.SpanID, flags)
}

// ParseTraceparent parses a version-00 W3C traceparent header value.
func ParseTraceparent(raw string) (SpanContext, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	parts := strings.Split(trimmed, "-")
	if len(parts) != 4 {
		return SpanContext{}, false
	}
	ver, traceID, spanID, flags := parts[0], parts[1], parts[2], parts[3]
	if ver != "00" || !isLowerHex(traceID, 32) || traceID == zeroTraceID ||
		!isLowerHex(spanID, 16) || spanID == zeroSpanID || !isLowerHex(flags, 2) {
		return SpanContext{}, false
	}
	return SpanContext{
		TraceID: traceID,
		SpanID:  spanID,
		Flags:   flags,
	}, true
}

// NormalizeTraceID validates a 32-char hex trace identifier and lowercases it.
func NormalizeTraceID(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if !isLowerHex(s, 32) || s == zeroTraceID {
		return "", false
	}
	return s, true
}

func isLowerHex(s string, wantLen int) bool {
	if len(s) != wantLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// ContextWithSpanContext attaches sc to ctx alongside the matching OpenTelemetry SpanContext.
func ContextWithSpanContext(ctx context.Context, sc SpanContext) context.Context {
	if !sc.Valid() {
		return ctx
	}
	if !isLowerHex(sc.Flags, 2) {
		sc.Flags = "01"
	}
	ctx = context.WithValue(ctx, spanCtxKey{}, sc)
	if otelSC, ok := toOTelSpanContext(sc, false); ok {
		ctx = oteltrace.ContextWithSpanContext(ctx, otelSC)
	}
	return ctx
}

// SpanContextFromContext returns the active SpanContext from OpenTelemetry or custom context state.
func SpanContextFromContext(ctx context.Context) (SpanContext, bool) {
	if ctx == nil {
		return SpanContext{}, false
	}
	// Prefer the innermost OpenTelemetry span when ADK Go v2 started a child span on ctx.
	if otelSC := oteltrace.SpanContextFromContext(ctx); otelSC.IsValid() {
		flags := "00"
		if otelSC.IsSampled() {
			flags = "01"
		}
		var ts string
		if prev, ok := ctx.Value(spanCtxKey{}).(SpanContext); ok {
			ts = prev.TraceState
		}
		return SpanContext{
			TraceID:    otelSC.TraceID().String(),
			SpanID:     otelSC.SpanID().String(),
			Flags:      flags,
			TraceState: ts,
		}, true
	}
	sc, ok := ctx.Value(spanCtxKey{}).(SpanContext)
	if !ok || !sc.Valid() {
		return SpanContext{}, false
	}
	return sc, true
}

// ExtractHTTP parses W3C traceparent and tracestate headers from h into ctx.
func ExtractHTTP(ctx context.Context, h http.Header) (context.Context, SpanContext, bool) {
	if h == nil {
		return ctx, SpanContext{}, false
	}
	sc, ok := ParseTraceparent(h.Get(traceparentHeader))
	if !ok {
		return ctx, SpanContext{}, false
	}
	sc.TraceState = strings.TrimSpace(h.Get(tracestateHeader))
	ctx = context.WithValue(ctx, spanCtxKey{}, sc)
	if otelSC, convOK := toOTelSpanContext(sc, true); convOK {
		ctx = oteltrace.ContextWithRemoteSpanContext(ctx, otelSC)
	}
	return ctx, sc, true
}

// InjectHTTP writes W3C traceparent (and tracestate if non-empty) from ctx into h.
func InjectHTTP(ctx context.Context, h http.Header) {
	if h == nil {
		return
	}
	sc, ok := SpanContextFromContext(ctx)
	if !ok {
		return
	}
	h.Set(traceparentHeader, sc.Traceparent())
	if sc.TraceState != "" {
		h.Set(tracestateHeader, sc.TraceState)
	}
}

func toOTelSpanContext(sc SpanContext, remote bool) (oteltrace.SpanContext, bool) {
	tidBytes, err1 := hex.DecodeString(sc.TraceID)
	sidBytes, err2 := hex.DecodeString(sc.SpanID)
	if err1 != nil || err2 != nil || len(tidBytes) != 16 || len(sidBytes) != 8 {
		return oteltrace.SpanContext{}, false
	}
	var tid oteltrace.TraceID
	var sid oteltrace.SpanID
	copy(tid[:], tidBytes)
	copy(sid[:], sidBytes)
	flags := oteltrace.FlagsSampled
	if sc.Flags == "00" {
		flags = 0
	}
	otelSC := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: flags,
		Remote:     remote,
	})
	return otelSC, otelSC.IsValid()
}

// WrapHTTPClient returns an *http.Client that injects W3C traceparent headers from
// each request's context and retries replayable requests across transient upstream
// connection resets without mutating hc.
func WrapHTTPClient(hc *http.Client, defaultTimeout time.Duration) *http.Client {
	if hc == nil {
		if defaultTimeout <= 0 {
			defaultTimeout = 30 * time.Second
		}
		return &http.Client{
			Transport: &propagationTransport{base: http.DefaultTransport},
			Timeout:   defaultTimeout,
		}
	}
	if _, already := hc.Transport.(*propagationTransport); already {
		return hc
	}
	cp := *hc
	base := cp.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	cp.Transport = &propagationTransport{base: base}
	return &cp
}

type propagationTransport struct {
	base http.RoundTripper
}

var envoyUpstreamResetBackoffs = [...]time.Duration{
	150 * time.Millisecond,
	400 * time.Millisecond,
}

func (t *propagationTransport) CloseIdleConnections() {
	closeIdleConnections(t.base)
}

func (t *propagationTransport) waitBeforeRetry(ctx context.Context, attempt int) error {
	t.CloseIdleConnections()
	return sleepWithContext(ctx, envoyUpstreamResetBackoffs[attempt])
}

func (t *propagationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	maxAttempts := 1
	if isReplayableRequest(req) {
		maxAttempts = 1 + len(envoyUpstreamResetBackoffs)
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		attemptReq := cloneWithTraceHeaders(req)
		if attempt > 0 && req.GetBody != nil {
			freshBody, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			attemptReq.Body = freshBody
		}

		resp, err := t.base.RoundTrip(attemptReq)
		if err != nil {
			if attempt+1 < maxAttempts && req.Context().Err() == nil && isTransientUpstreamError(err) {
				if waitErr := t.waitBeforeRetry(req.Context(), attempt); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			return nil, err
		}

		if attempt+1 < maxAttempts && isGatewayResetStatus(resp.StatusCode) {
			peek, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			if isEnvoyUpstreamResetBody(peek) {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				_ = resp.Body.Close()
				if waitErr := t.waitBeforeRetry(req.Context(), attempt); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			resp.Body = &restoredReadCloser{
				Reader: io.MultiReader(bytes.NewReader(peek), resp.Body),
				Closer: resp.Body,
			}
		}
		return resp, nil
	}
	return nil, errors.New("unreachable transport retry exit")
}

func cloneWithTraceHeaders(req *http.Request) *http.Request {
	cloned := req.Clone(req.Context())
	if cloned.Header.Get(traceparentHeader) != "" {
		return cloned
	}
	sc, ok := SpanContextFromContext(req.Context())
	if !ok {
		return cloned
	}
	cloned.Header.Set(traceparentHeader, sc.Traceparent())
	if sc.TraceState != "" && cloned.Header.Get(tracestateHeader) == "" {
		cloned.Header.Set(tracestateHeader, sc.TraceState)
	}
	return cloned
}

func isReplayableRequest(req *http.Request) bool {
	if req == nil {
		return false
	}
	if req.GetBody != nil {
		return true
	}
	return (req.Method == http.MethodGet || req.Method == http.MethodHead) &&
		(req.Body == nil || req.Body == http.NoBody)
}

func isGatewayResetStatus(code int) bool {
	return code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable ||
		code == http.StatusGatewayTimeout
}

func isEnvoyUpstreamResetBody(peek []byte) bool {
	if len(peek) == 0 {
		return false
	}
	s := strings.ToLower(string(peek))
	return strings.Contains(s, "upstream connect error or disconnect/reset before headers") ||
		strings.Contains(s, "reset reason: remote connection failure") ||
		strings.Contains(s, "reset reason: connection termination")
}

func isTransientUpstreamError(err error) bool {
	if errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "server closed idle connection") ||
		strings.HasSuffix(msg, ": eof")
}

type idleConnCloser interface {
	CloseIdleConnections()
}

func closeIdleConnections(rt http.RoundTripper) {
	if ic, ok := rt.(idleConnCloser); ok {
		ic.CloseIdleConnections()
	}
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type restoredReadCloser struct {
	io.Reader
	io.Closer
}
