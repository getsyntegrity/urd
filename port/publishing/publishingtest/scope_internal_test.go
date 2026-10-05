// MIT License
//
// Copyright (c) 2022-2026 Arsene Tochemey Gandote
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package publishingtest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/port/publishing"
)

// PT-4 self-checks: the fakes below stand in for a broker adapter that records
// the scope of the context it is given (for example as a header).

type scopeKey struct{}

func withScopeValue(parent context.Context, tenant string) (context.Context, error) {
	return context.WithValue(parent, scopeKey{}, tenant), nil
}

type scopeMode int

const (
	scopeCarried scopeMode = iota // records the tenant of the context it is given
	scopeBlind                    // records no scope at all
	scopeStuck                    // records one fixed tenant whatever the context says
)

// scopeBackend is the shared "broker": every publisher a target builds records
// into it, and ScopeOf reads it back.
type scopeBackend struct {
	mu     sync.Mutex
	scopes map[string]string // persistence id -> recorded scope
}

func (b *scopeBackend) scopeOf(id string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	scope, ok := b.scopes[id]
	if !ok {
		return "", errors.New("never recorded")
	}
	return scope, nil
}

// scopeRecorder is both an events and a state publisher.
type scopeRecorder struct {
	mode    scopeMode
	backend *scopeBackend

	mu     sync.Mutex
	closed bool
}

func newBackend() *scopeBackend { return &scopeBackend{scopes: map[string]string{}} }

func (b *scopeBackend) publisher(mode scopeMode) *scopeRecorder {
	return &scopeRecorder{mode: mode, backend: b}
}

func (r *scopeRecorder) ID() string { return "scope-recorder" }

func (r *scopeRecorder) Close(context.Context) error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return nil
}

func (r *scopeRecorder) record(ctx context.Context, id string) error {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return publishing.ErrPublisherNotStarted
	}
	scope := ""
	switch r.mode {
	case scopeCarried:
		scope, _ = ctx.Value(scopeKey{}).(string)
	case scopeStuck:
		scope = "tenant-a"
	}
	r.backend.mu.Lock()
	r.backend.scopes[id] = scope
	r.backend.mu.Unlock()
	return nil
}

func (r *scopeRecorder) Publish(ctx context.Context, e *egopb.Event) error {
	return r.record(ctx, e.GetPersistenceId())
}

type stateScopeRecorder struct{ *scopeRecorder }

func (r stateScopeRecorder) Publish(ctx context.Context, s *egopb.DurableState) error {
	return r.record(ctx, s.GetPersistenceId())
}

func scopedEventsTarget(mode scopeMode, withHooks bool) EventsTarget {
	rec := newBackend()
	target := EventsTarget{
		New: func(*testing.T) (publishing.EventPublisher, error) { return rec.publisher(mode), nil },
		Received: func(_ context.Context, _ *testing.T, want *egopb.Event) error {
			_, err := rec.scopeOf(want.GetPersistenceId())
			return err
		},
	}
	if withHooks {
		target.WithScope = withScopeValue
		target.ScopeOf = func(_ context.Context, _ *testing.T, want *egopb.Event) (string, error) {
			return rec.scopeOf(want.GetPersistenceId())
		}
	}
	return target
}

func scopedStateTarget(mode scopeMode) StateTarget {
	rec := newBackend()
	return StateTarget{
		New: func(*testing.T) (publishing.StatePublisher, error) {
			return stateScopeRecorder{rec.publisher(mode)}, nil
		},
		Received: func(_ context.Context, _ *testing.T, want *egopb.DurableState) error {
			_, err := rec.scopeOf(want.GetPersistenceId())
			return err
		},
		WithScope: withScopeValue,
		ScopeOf: func(_ context.Context, _ *testing.T, want *egopb.DurableState) (string, error) {
			return rec.scopeOf(want.GetPersistenceId())
		},
	}
}

func TestCapture_PT4(t *testing.T) {
	specs.Describe(t, "PT-4 proves a publisher carries the scope of the context it is handed", func(s *specs.Spec) {
		s.It("passes for a publisher that records the context's tenant, events and state alike", func(ctx *specs.Context) {
			got := byCheck(ctx.T, captureEvents(ctx.T, scopedEventsTarget(scopeCarried, true)))
			ctx.Expect(got).To(resultFor("PT-4", Passed, ""))
			gotState := byCheck(ctx.T, captureState(ctx.T, scopedStateTarget(scopeCarried)))
			ctx.Expect(gotState).To(resultFor("PT-4", Passed, ""))
		})

		s.It("fails only PT-4 for a publisher that drops the scope", func(ctx *specs.Context) {
			got := byCheck(ctx.T, captureEvents(ctx.T, scopedEventsTarget(scopeBlind, true)))
			ctx.Expect(got).To(onlyFailure("PT-4", "recorded for scope"))
			gotState := byCheck(ctx.T, captureState(ctx.T, scopedStateTarget(scopeBlind)))
			ctx.Expect(gotState).To(onlyFailure("PT-4", "recorded for scope"))
		})

		s.It("fails only PT-4 for a publisher that attributes everything to one tenant", func(ctx *specs.Context) {
			got := byCheck(ctx.T, captureEvents(ctx.T, scopedEventsTarget(scopeStuck, true)))
			ctx.Expect(got).To(onlyFailure("PT-4", "tenant-b"))
		})

		s.It("is omitted, not reported, when the target sets no scope hooks", func(ctx *specs.Context) {
			results := captureEvents(ctx.T, scopedEventsTarget(scopeCarried, false))
			for _, r := range results {
				ctx.Expect(r.Check).To(specs.NotEqual("PT-4"))
			}
			ctx.Expect(results).To(specs.HaveLen(3))
		})
	})
}
