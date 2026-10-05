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

// Package publishingtest is the conformance suite for publishers: the
// publisher-specific rules of ego-arch-004 design §D8, which every
// publishing.EventPublisher and publishing.StatePublisher runs in its own
// tests next to port/adapter/adaptertest.
//
//   - PT-1 (L5): Publish after Close returns an error matching
//     publishing.ErrPublisherNotStarted, and does not block.
//   - PT-2: ID() is non-empty and stable across calls.
//   - PT-3: a published message reaches the caller-supplied observer
//     (EventsTarget.Received, StateTarget.Received).
//   - PT-4 (EGO-TENANT-005): a publisher handed a context that carries a
//     tenant scope delivers the message attributed to that tenant, and to no
//     other. It runs only when the target sets both WithScope and ScopeOf; with
//     either unset it is omitted, never reported, so existing adopters see no
//     new result.
//
// # Skipped and not exercised
//
// A check is skipped only when New returns an error that matches
// port/adapter/adaptertest.ErrUnreachable. This package may not import
// adaptertest, so it recognizes that error by the method it has,
// Unreachable() bool, found with errors.As; wrap adaptertest.ErrUnreachable
// and both suites skip. Any other error, or a nil or typed-nil publisher,
// fails the check. PT-3 without a Received hook is reported as "not
// exercised", never as passed, and gets no subtest.
//
// This package imports only the standard library, port/publishing and
// egopb, and deliberately not port/adapter, so it can move into the
// ego-arch-006 contracts module together with port/publishing.
package publishingtest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/port/publishing"
)

// EventsTarget describes an events publisher under test.
type EventsTarget struct {
	// New returns a fresh publisher for one check. Wrap
	// adaptertest.ErrUnreachable to skip.
	New func(t *testing.T) (publishing.EventPublisher, error)
	// Received optionally blocks until the backend has received an event
	// equal to want, and returns nil, or returns an error when ctx ends
	// first (PT-3).
	Received func(ctx context.Context, t *testing.T, want *egopb.Event) error
	// WithScope optionally returns parent carrying the tenant scope the way
	// the engine hands it to a publisher (for Urd, a tenancy.Attach'ed
	// context). It keeps this package free of the tenancy dependency (PT-4).
	WithScope func(parent context.Context, tenant string) (context.Context, error)
	// ScopeOf optionally reports the tenant the backend recorded for want
	// (for example a header or a topic suffix), "" when it recorded none, or
	// an error when ctx ends first (PT-4).
	ScopeOf func(ctx context.Context, t *testing.T, want *egopb.Event) (string, error)
}

// StateTarget describes a durable state publisher under test.
type StateTarget struct {
	// New returns a fresh publisher for one check. Wrap
	// adaptertest.ErrUnreachable to skip.
	New func(t *testing.T) (publishing.StatePublisher, error)
	// Received optionally blocks until the backend has received a state
	// equal to want, and returns nil, or returns an error when ctx ends
	// first (PT-3).
	Received func(ctx context.Context, t *testing.T, want *egopb.DurableState) error
	// WithScope is the StateTarget counterpart of EventsTarget.WithScope.
	WithScope func(parent context.Context, tenant string) (context.Context, error)
	// ScopeOf is the StateTarget counterpart of EventsTarget.ScopeOf.
	ScopeOf func(ctx context.Context, t *testing.T, want *egopb.DurableState) (string, error)
}

// RunEvents runs PT-1…PT-3 against an events publisher, each exercised
// check as a subtest of t, and returns one Result per check, so an adopter
// can assert its exact outcome set as it does with adaptertest.Run.
func RunEvents(t *testing.T, target EventsTarget) []Result {
	t.Helper()
	return run(t, eventsSuite(target))
}

// RunState runs PT-1…PT-3 against a durable state publisher, each
// exercised check as a subtest of t, and returns one Result per check.
func RunState(t *testing.T, target StateTarget) []Result {
	t.Helper()
	return run(t, stateSuite(target))
}

// opTimeout bounds every publisher call; a call that has not returned by
// opTimeout+grace fails the check (its goroutine is left behind).
const (
	opTimeout = 5 * time.Second
	grace     = time.Second
)

// publisher is what EventPublisher and StatePublisher have in common.
type publisher[M any] interface {
	ID() string
	Publish(ctx context.Context, msg M) error
	Close(ctx context.Context) error
}

type suite[M any] struct {
	newPublisher func(t *testing.T) (publisher[M], error)
	received     func(ctx context.Context, t *testing.T, want M) error
	withScope    func(parent context.Context, tenant string) (context.Context, error)
	scopeOf      func(ctx context.Context, t *testing.T, want M) (string, error)
	sample       func(id string) M
}

func eventsSuite(target EventsTarget) suite[*egopb.Event] {
	s := suite[*egopb.Event]{
		sample: func(id string) *egopb.Event {
			return &egopb.Event{PersistenceId: id, SequenceNumber: 1, Timestamp: time.Now().UnixMilli()}
		},
		received:  target.Received,
		withScope: target.WithScope,
		scopeOf:   target.ScopeOf,
	}
	if target.New != nil {
		s.newPublisher = func(t *testing.T) (publisher[*egopb.Event], error) {
			p, err := target.New(t)
			return p, err
		}
	}
	return s
}

func stateSuite(target StateTarget) suite[*egopb.DurableState] {
	s := suite[*egopb.DurableState]{
		sample: func(id string) *egopb.DurableState {
			return &egopb.DurableState{PersistenceId: id, VersionNumber: 1, Timestamp: time.Now().UnixMilli()}
		},
		received:  target.Received,
		withScope: target.WithScope,
		scopeOf:   target.ScopeOf,
	}
	if target.New != nil {
		s.newPublisher = func(t *testing.T) (publisher[*egopb.DurableState], error) {
			p, err := target.New(t)
			return p, err
		}
	}
	return s
}

// tb is the part of testing.TB the checks use; *testing.T implements it,
// and so does recorder for the package's self-checks.
type tb interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Skipf(format string, args ...any)
	Logf(format string, args ...any)
}

type check[M any] struct {
	name string
	// applies, when set and false, omits the check altogether: no subtest and
	// no Result (PT-4, so adopters written before it see an unchanged result set).
	applies      func(s suite[M]) bool
	notExercised func(s suite[M]) string
	run          func(s suite[M], t tb, ft *testing.T)
}

func checksFor[M any]() []check[M] {
	return []check[M]{
		{name: "PT-1", run: func(s suite[M], t tb, ft *testing.T) {
			p := s.mustNew(t, ft)
			if ok, err := bounded(func(ctx context.Context) error { return p.Close(ctx) }); !ok || err != nil {
				t.Errorf("Close = %v (returned: %v), want nil", err, ok)
			}
			ok, err := bounded(func(ctx context.Context) error { return p.Publish(ctx, s.sample(uniqueID())) })
			switch {
			case !ok:
				t.Errorf("Publish after Close blocked for more than %v (L5)", opTimeout+grace)
			case !errors.Is(err, publishing.ErrPublisherNotStarted):
				t.Errorf("Publish after Close = %v, want an error matching publishing.ErrPublisherNotStarted (L5)", err)
			}
		}},
		{name: "PT-2", run: func(s suite[M], t tb, ft *testing.T) {
			p := s.mustNew(t, ft)
			defer closeQuietly(p)
			first, second := p.ID(), p.ID()
			if first == "" {
				t.Errorf("ID() is empty")
			}
			if first != second {
				t.Errorf("ID() is not stable across calls: %q, then %q", first, second)
			}
		}},
		{name: "PT-3", notExercised: func(s suite[M]) string {
			if s.received == nil {
				return "no hook: Received is not set"
			}
			return ""
		}, run: func(s suite[M], t tb, ft *testing.T) {
			p := s.mustNew(t, ft)
			defer closeQuietly(p)
			want := s.sample(uniqueID())
			ok, err := bounded(func(ctx context.Context) error { return p.Publish(ctx, want) })
			switch {
			case !ok:
				t.Fatalf("Publish did not return within %v", opTimeout+grace)
			case err != nil:
				t.Fatalf("Publish = %v, want nil", err)
			}
			ok, err = bounded(func(ctx context.Context) error { return s.received(ctx, ft, want) })
			switch {
			case !ok:
				t.Errorf("Received did not return within %v", opTimeout+grace)
			case err != nil:
				t.Errorf("the published message did not reach the observer: %v", err)
			}
		}},
		{name: "PT-4", applies: func(s suite[M]) bool { return s.withScope != nil && s.scopeOf != nil },
			run: func(s suite[M], t tb, ft *testing.T) {
				p := s.mustNew(t, ft)
				defer closeQuietly(p)
				for _, tenant := range []string{"tenant-a", "tenant-b"} {
					pubCtx, err := s.withScope(context.Background(), tenant)
					if err != nil {
						t.Fatalf("WithScope(%q) = %v, want nil", tenant, err)
					}
					want := s.sample(uniqueID())
					ok, err := bounded(func(context.Context) error { return p.Publish(pubCtx, want) })
					switch {
					case !ok:
						t.Fatalf("Publish for %q did not return within %v", tenant, opTimeout+grace)
					case err != nil:
						t.Fatalf("Publish for %q = %v, want nil", tenant, err)
					}
					var got string
					ok, err = bounded(func(ctx context.Context) (e error) { got, e = s.scopeOf(ctx, ft, want); return e })
					switch {
					case !ok:
						t.Fatalf("ScopeOf did not return within %v", opTimeout+grace)
					case err != nil:
						t.Errorf("the backend did not record a scope for the message published for %q: %v", tenant, err)
					case got != tenant:
						t.Errorf("the message published for %q was recorded for scope %q: the publisher must carry the scope of the context it is handed, and no other", tenant, got)
					}
				}
			}},
	}
}

var sequence atomic.Uint64

func uniqueID() string {
	return fmt.Sprintf("publishingtest-%d-%d", time.Now().UnixNano(), sequence.Add(1))
}

func run[M any](t *testing.T, s suite[M]) []Result {
	t.Helper()
	if s.newPublisher == nil {
		t.Fatalf("publishingtest: invalid target: New is nil")
	}
	var summary strings.Builder
	summary.WriteString("publishingtest summary:")
	checks := checksFor[M]()
	results := make([]Result, 0, len(checks))
	for _, c := range checks {
		if c.applies != nil && !c.applies(s) {
			continue
		}
		if c.notExercised != nil {
			if reason := c.notExercised(s); reason != "" {
				t.Logf("%s: not exercised: %s", c.name, reason)
				fmt.Fprintf(&summary, "\n  %-6s not exercised: %s", c.name, reason)
				results = append(results, Result{Check: c.name, Outcome: NotExercised, Detail: reason})
				continue
			}
		}
		var sub *testing.T
		t.Run(c.name, func(st *testing.T) {
			sub = st
			c.run(s, st, st)
		})
		o := outcomeOf(sub)
		fmt.Fprintf(&summary, "\n  %-6s %s", c.name, o)
		results = append(results, Result{Check: c.name, Outcome: o})
	}
	t.Log(summary.String())
	return results
}

func outcomeOf(t *testing.T) Outcome {
	switch {
	case t == nil || t.Failed():
		return Failed
	case t.Skipped():
		return Skipped
	default:
		return Passed
	}
}

// mustNew calls New and applies the skip rule.
func (s suite[M]) mustNew(t tb, ft *testing.T) publisher[M] {
	t.Helper()
	p, err := s.newPublisher(ft)
	var marker interface{ Unreachable() bool }
	if errors.As(err, &marker) && marker.Unreachable() {
		t.Skipf("New: %v", err)
	}
	if err != nil {
		t.Fatalf("New returned an error that does not match adaptertest.ErrUnreachable, so the check fails instead of skipping: %v", err)
	}
	if isNil(p) {
		t.Fatalf("New returned a nil publisher (%T)", p)
	}
	return p
}

func closeQuietly[M any](p publisher[M]) {
	_, _ = bounded(func(ctx context.Context) error { return p.Close(ctx) })
}

// bounded calls fn with a context that expires after opTimeout and waits
// at most opTimeout+grace. returned is false when fn did not return in
// time.
func bounded(fn func(ctx context.Context) error) (returned bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fn(ctx) }()
	timer := time.NewTimer(opTimeout + grace)
	defer timer.Stop()
	select {
	case err := <-done:
		return true, err
	case <-timer.C:
		return false, nil
	}
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return rv.IsNil()
	default:
		return false
	}
}

// Outcome is the result of one check. It mirrors adaptertest.Outcome,
// which this package may not import.
type Outcome int

const (
	// Passed means the check ran and found nothing wrong.
	Passed Outcome = iota + 1
	// Failed means the check ran and found a violation, or could not run
	// because of a broken target.
	Failed
	// Skipped means New returned an error matching
	// adaptertest.ErrUnreachable.
	Skipped
	// NotExercised means the suite could not run the check because a hook
	// is missing. It is never a pass.
	NotExercised
)

// String returns the outcome in words, as the summary prints it.
func (o Outcome) String() string {
	switch o {
	case Passed:
		return "passed"
	case Failed:
		return "failed"
	case Skipped:
		return "skipped"
	case NotExercised:
		return "not exercised"
	default:
		return fmt.Sprintf("outcome(%d)", int(o))
	}
}

// Result reports one check.
type Result struct {
	// Check is the check's name, for example "PT-1".
	Check string
	// Outcome is what happened.
	Outcome Outcome
	// Detail says why a check was not exercised and, from the package's
	// own capture, the failure messages.
	Detail string
}

func captureEvents(t *testing.T, target EventsTarget) []Result {
	return capture(t, eventsSuite(target))
}

func captureState(t *testing.T, target StateTarget) []Result {
	return capture(t, stateSuite(target))
}

// capture runs the checks without failing t, recording each outcome. The
// target's New and Received receive t but run on a goroutine of capture's
// own, not t's test goroutine, so they must not call t.Fatal, t.FailNow,
// t.Skip or t.SkipNow; they report failure by returning an error.
func capture[M any](t *testing.T, s suite[M]) []Result {
	var out []Result
	for _, c := range checksFor[M]() {
		if c.applies != nil && !c.applies(s) {
			continue
		}
		if s.newPublisher == nil {
			out = append(out, Result{Check: c.name, Outcome: Failed, Detail: "New is nil"})
			continue
		}
		if c.notExercised != nil {
			if reason := c.notExercised(s); reason != "" {
				out = append(out, Result{Check: c.name, Outcome: NotExercised, Detail: reason})
				continue
			}
		}
		o, detail := record(func(r tb) { c.run(s, r, t) })
		out = append(out, Result{Check: c.name, Outcome: o, Detail: detail})
	}
	return out
}

// recorder implements tb by recording. Fatalf and Skipf end the calling
// goroutine with runtime.Goexit, as *testing.T does, so record runs fn in
// a goroutine of its own.
type recorder struct {
	failed, skipped bool
	messages        []string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.failed = true
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
}
func (r *recorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	runtime.Goexit()
}
func (r *recorder) Skipf(format string, args ...any) {
	r.skipped = true
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
	runtime.Goexit()
}
func (r *recorder) Logf(string, ...any) {}

func record(fn func(r tb)) (Outcome, string) {
	r := &recorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(r)
	}()
	<-done
	detail := strings.Join(r.messages, "; ")
	switch {
	case r.failed:
		return Failed, detail
	case r.skipped:
		return Skipped, detail
	default:
		return Passed, detail
	}
}
