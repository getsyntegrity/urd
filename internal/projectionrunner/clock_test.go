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

package projectionrunner

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/mock"
	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/atomic"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/projection"
	testkit2 "github.com/getsyntegrity/urd/testkit"
)

// manualClock adapts a go-specs ManualClock to the runner's clock and records
// the duration of every timer the runner arms, so a test can assert the delay
// the runner chose, not only that some timer fired. A test moves time with
// Advance and waits for the runner to arm a timer with awaitTimer, so an
// advance is never lost to a timer that does not exist yet.
type manualClock struct {
	*specs.ManualClock

	mu     sync.Mutex
	delays []time.Duration
}

var _ clock = (*manualClock)(nil)

func newManualClock() *manualClock { return &manualClock{ManualClock: specs.NewManualClock()} }

// NewTimer records d and arms a timer on the manual clock. The go-specs timer
// already has the method set of the runner's timer.
func (m *manualClock) NewTimer(d time.Duration) timer {
	m.mu.Lock()
	m.delays = append(m.delays, d)
	m.mu.Unlock()
	return m.ManualClock.NewTimer(d)
}

// timers returns the duration of every timer armed so far, in order.
func (m *manualClock) timers() []time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]time.Duration(nil), m.delays...)
}

// awaitTimer waits until the runner has exactly one timer pending.
//
// It assumes the runner arms one timer at a time, which holds between passes.
// It breaks during a nudge pass: the pull timer stays armed while the pass
// runs, so a store backoff or a handler retry inside that pass makes two
// timers pending, and awaitTimer would then never see exactly one. A test that
// waits inside a nudge pass must wait for the count it expects with
// awaitTimers instead.
func awaitTimer(ctx *specs.Context, clk *manualClock) {
	awaitTimers(ctx, clk, 1)
}

// awaitTimers waits until the runner has exactly n timers pending.
func awaitTimers(ctx *specs.Context, clk *manualClock, n int) {
	ctx.Eventually(func() any { return clk.Pending() }, specs.Equal(n), poll...)
}

// startOnClock runs Start in a task and drives its ping retries: it advances the
// clock by the retry delay each time Start waits, then returns the error Start
// failed with. Start is expected to exhaust its five attempts.
func startOnClock(ctx *specs.Context, runner *Runner, clk *manualClock) error {
	// A Start that never finishes would block the spec forever, since a task is
	// never cancelled: ending the context on any exit unblocks it.
	startCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan error, 1)
	ctx.Go(func(*specs.Context) { started <- runner.Start(startCtx) })

	for range 4 {
		awaitTimer(ctx, clk)
		clk.Advance(time.Second)
	}

	return awaitFailure(ctx, started)
}

// clockedStores stubs the two stores for a runner that never reaches a real
// shard: both answer Ping, and ShardOffsets answers shardOffsets and counts
// its calls in pulls.
func clockedStores(ctx *specs.Context, pulls *atomic.Int32, shardOffsets func() []any) (*enginetest.EventsStoreMock, *enginetest.OffsetStoreMock) {
	offsetCtrl := mock.NewController(ctx)
	offsetCtrl.Method("Ping").Expect(mock.Any()).Return(nil).AtLeast(1)

	eventsCtrl := mock.NewController(ctx)
	eventsCtrl.Method("Ping").Expect(mock.Any()).Return(nil).AtLeast(1)
	eventsCtrl.Method("ShardOffsets").Expect(mock.Any(), runnerTestScope).AtLeast(1).
		Do(func([]any) []any { pulls.Inc(); return shardOffsets() })

	return enginetest.NewEventsStoreMock(eventsCtrl), enginetest.NewOffsetStoreMock(offsetCtrl)
}

// seededShard is a shard holding one event, written to testkit stores. The
// committed offset is written only when it is not zero.
func seededShard(ctx *specs.Context, name string, shard uint64, committed, eventTimestamp int64) (*testkit2.EventStore, *testkit2.OffsetStore) {
	bg := context.TODO()
	events := testkit2.NewEventsStore()
	ctx.Expect(events.Connect(bg)).To(specs.BeNil())
	offsets := testkit2.NewOffsetStore()
	ctx.Expect(offsets.Connect(bg)).To(specs.BeNil())

	if committed != 0 {
		ctx.Expect(offsets.WriteOffset(bg, &egopb.Offset{ProjectionName: name, ShardNumber: shard, Value: committed})).To(specs.BeNil())
	}

	event, err := anypb.New(&testpb.AccountCredited{})
	ctx.Expect(err).To(specs.BeNil())
	journal := []*egopb.Event{{
		PersistenceId:  uuid.NewString(),
		SequenceNumber: 1,
		Event:          event,
		Timestamp:      eventTimestamp,
		Shard:          shard,
	}}
	ctx.Expect(events.WriteEvents(bg, runnerTestScope, journal, persistence.Unconditional())).To(specs.BeNil())

	return events, offsets
}

// lagOf observes the lag gauge the runner recorded.
func lagOf(reader *sdkmetric.ManualReader) func() any {
	return func() any {
		var collected metricdata.ResourceMetrics
		if err := reader.Collect(context.TODO(), &collected); err != nil {
			return err
		}
		for _, scope := range collected.ScopeMetrics {
			for _, metric := range scope.Metrics {
				gauge, ok := metric.Data.(metricdata.Gauge[int64])
				if metric.Name == "urd.projection.lag_ms" && ok && len(gauge.DataPoints) > 0 {
					return gauge.DataPoints[0].Value
				}
			}
		}
		return nil
	}
}

// failingHandler fails every event and counts the attempts.
type failingHandler struct{ calls *atomic.Int32 }

func (h failingHandler) Handle(context.Context, string, *anypb.Any, uint64) error {
	h.calls.Inc()
	return errFailed
}

func TestWithClock(t *testing.T) {
	specs.Describe(t, "the runner reads time through an injectable clock", func(s *specs.Spec) {
		s.It("uses the clock given to WithClock", func(ctx *specs.Context) {
			manual := newManualClock()
			var r Runner
			WithClock(manual).Apply(&r)
			ctx.Expect(r.clock).To(specs.Equal(manual))
		})
		s.It("defaults New to the real clock", func(ctx *specs.Context) {
			runner := New("clock-default", nil, nil, nil, WithScope(runnerTestScope))
			ctx.Expect(runner.clock).To(specs.Equal(realClock{}))
		})
		s.It("keeps the real clock when WithClock is given nil", func(ctx *specs.Context) {
			runner := New("clock-nil", nil, nil, nil, WithScope(runnerTestScope), WithClock(nil))
			ctx.Expect(runner.clock).To(specs.Equal(realClock{}))
		})
		s.It("has a real clock that tells the wall time and fires its timers", func(ctx *specs.Context) {
			before := time.Now()
			now := realClock{}.Now()
			ctx.Expect(now.Before(before)).To(specs.BeFalse())

			fired := realClock{}.NewTimer(time.Millisecond)
			defer fired.Stop()
			ctx.Eventually(func() any {
				select {
				case <-fired.C():
					return true
				default:
					return false
				}
			}, specs.BeTrue(), poll...)

			stopped := realClock{}.NewTimer(time.Hour)
			ctx.Expect(stopped.Stop()).To(specs.BeTrue())
		})
	})
}

func TestRunnerOnAManualClock(t *testing.T) {
	const interval = 10 * time.Second

	specs.Describe(t, "the runner waits only on the clock it was given", func(s *specs.Spec) {
		s.It("pulls once per interval of the clock", func(ctx *specs.Context) {
			bg := context.TODO()
			clk := newManualClock()
			pulls := atomic.NewInt32(0)
			eventsStore, offsetStore := clockedStores(ctx, pulls, func() []any { return []any{nil, nil} })
			runner := New("clock-pull", projection.NewDiscardHandler(), eventsStore, offsetStore, WithScope(runnerTestScope),
				WithPullInterval(interval), WithClock(clk))

			ctx.Expect(runner.Start(bg)).To(specs.BeNil())
			runner.Run(bg, nil)
			awaitTimer(ctx, clk)

			// just short of an interval the timer is still armed and has not fired
			clk.Advance(interval - time.Millisecond)
			ctx.Expect(clk.Pending()).To(specs.Equal(1))

			clk.Advance(time.Millisecond)
			ctx.Eventually(counter(pulls), specs.Equal(int32(1)), poll...)
			awaitTimer(ctx, clk)
			ctx.Expect(pulls.Load()).To(specs.Equal(int32(1)))

			clk.Advance(interval)
			ctx.Eventually(counter(pulls), specs.Equal(int32(2)), poll...)
			awaitTimer(ctx, clk)
			ctx.Expect(clk.timers()).To(specs.Equal([]time.Duration{interval, interval, interval}))

			// stopping the runner leaves no timer behind
			ctx.Expect(runner.Stop()).To(specs.BeNil())
			ctx.Eventually(func() any { return clk.Pending() }, specs.Equal(0), poll...)
		})

		s.It("re-arms the interval timer after a pass triggered by a nudge", func(ctx *specs.Context) {
			bg := context.TODO()
			clk := newManualClock()
			pulls := atomic.NewInt32(0)
			eventsStore, offsetStore := clockedStores(ctx, pulls, func() []any { return []any{nil, nil} })
			runner := New("clock-nudge", projection.NewDiscardHandler(), eventsStore, offsetStore, WithScope(runnerTestScope),
				WithPullInterval(interval), WithClock(clk))

			ctx.Expect(runner.Start(bg)).To(specs.BeNil())
			runner.Run(bg, nil)
			awaitTimer(ctx, clk)

			// halfway through the interval the first timer is due in another half
			clk.Advance(interval / 2)
			ctx.Expect(pulls.Load()).To(specs.Equal(int32(0)))

			// a nudge runs a pass now; it ends by replacing the first timer with a
			// fresh one that is due a whole interval after the nudge
			runner.requestPull()
			ctx.Eventually(counter(pulls), specs.Equal(int32(1)), poll...)
			ctx.Eventually(func() any { return len(clk.timers()) }, specs.Equal(2), poll...)
			awaitTimer(ctx, clk)

			// another half interval reaches the instant the first timer was due. Had
			// it survived it would fire now, so no pull and one pending timer show it
			// was replaced. The state is held for a while, not read once, since a
			// late pull would otherwise be missed.
			clk.Advance(interval / 2)
			ctx.Consistently(func() any { return []int{int(pulls.Load()), clk.Pending(), len(clk.timers())} },
				specs.Equal([]int{1, 1, 2}), specs.WithTimeout(300*time.Millisecond), specs.WithInterval(waitInterval))

			// a whole interval after the nudge the fresh timer fires
			clk.Advance(interval / 2)
			ctx.Eventually(counter(pulls), specs.Equal(int32(2)), poll...)
			ctx.Expect(clk.timers()[:2]).To(specs.Equal([]time.Duration{interval, interval}))

			ctx.Expect(runner.Stop()).To(specs.BeNil())
		})

		s.It("backs off on the clock after a failed store round trip", func(ctx *specs.Context) {
			bg := context.TODO()
			clk := newManualClock()
			pulls := atomic.NewInt32(0)
			eventsStore, offsetStore := clockedStores(ctx, pulls, func() []any { return []any{nil, errFailed} })
			runner := New("clock-backoff", projection.NewDiscardHandler(), eventsStore, offsetStore, WithScope(runnerTestScope),
				WithPullInterval(interval), WithClock(clk))

			ctx.Expect(runner.Start(bg)).To(specs.BeNil())
			runner.Run(bg, nil)
			awaitTimer(ctx, clk)

			// the first failure waits one second, then the next pull waits for the interval
			clk.Advance(interval)
			ctx.Eventually(counter(pulls), specs.Equal(int32(1)), poll...)
			awaitTimer(ctx, clk)
			clk.Advance(time.Second)
			awaitTimer(ctx, clk)

			// the second consecutive failure waits twice as long
			clk.Advance(interval)
			ctx.Eventually(counter(pulls), specs.Equal(int32(2)), poll...)
			awaitTimer(ctx, clk)

			ctx.Expect(clk.timers()).To(specs.Equal([]time.Duration{
				interval, time.Second, interval, 2 * time.Second,
			}))
			ctx.Expect(runner.Stop()).To(specs.BeNil())
		})

		s.It("pings the stores five times, one second apart, before Start fails", func(ctx *specs.Context) {
			clk := newManualClock()
			pings := atomic.NewInt32(0)

			eventsCtrl := mock.NewController(ctx)
			eventsCtrl.Method("Ping").Expect(mock.Any()).AtLeast(1).
				Do(func([]any) []any { pings.Inc(); return []any{errors.New("fail ping")} })
			offsetCtrl := mock.NewController(ctx)
			offsetCtrl.Method("Ping").Expect(mock.Any()).Return(nil).AtLeast(1)

			runner := New("clock-ping", projection.NewDiscardHandler(), enginetest.NewEventsStoreMock(eventsCtrl), enginetest.NewOffsetStoreMock(offsetCtrl), WithScope(runnerTestScope),
				WithClock(clk))

			ctx.Expect(startOnClock(ctx, runner, clk)).To(haveMessage("failed to start the projection: fail ping"))
			ctx.Expect(pings.Load()).To(specs.Equal(int32(5)))
			ctx.Expect(clk.timers()).To(specs.Equal([]time.Duration{time.Second, time.Second, time.Second, time.Second}))
		})

		s.It("retries a failing handler on the clock before it skips the event", func(ctx *specs.Context) {
			bg := context.TODO()
			const name, shard = "clock-recovery", uint64(4)
			clk := newManualClock()
			eventTimestamp := clk.Now().Add(time.Second).UnixNano()
			eventsStore, offsetStore := seededShard(ctx, name, shard, 0, eventTimestamp)

			attempts := atomic.NewInt32(0)
			runner := New(name, failingHandler{attempts}, eventsStore, offsetStore, WithScope(runnerTestScope),
				WithPullInterval(interval), WithClock(clk),
				WithRecoveryStrategy(projection.NewRecovery(
					projection.WithRecoveryPolicy(projection.RetryAndSkip),
					projection.WithRetries(3),
					projection.WithRetryDelay(500*time.Millisecond))))

			ctx.Expect(runner.Start(bg)).To(specs.BeNil())
			runner.Run(bg, nil)
			awaitTimer(ctx, clk)

			clk.Advance(interval)
			ctx.Eventually(counter(attempts), specs.Equal(int32(1)), poll...)
			awaitTimer(ctx, clk)
			clk.Advance(500 * time.Millisecond)
			ctx.Eventually(counter(attempts), specs.Equal(int32(2)), poll...)
			awaitTimer(ctx, clk)
			clk.Advance(500 * time.Millisecond)

			// the third attempt is the last: the event is skipped and its offset committed
			projectionID := &egopb.ProjectionId{ProjectionName: name, ShardNumber: shard}
			ctx.Eventually(offsetOf(offsetStore, projectionID), committedAt(eventTimestamp), poll...)
			ctx.Expect(attempts.Load()).To(specs.Equal(int32(3)))
			ctx.Expect(clk.timers()[:3]).To(specs.Equal([]time.Duration{interval, 500 * time.Millisecond, 500 * time.Millisecond}))

			ctx.Expect(runner.Stop()).To(specs.BeNil())
		})

		s.It("stamps the committed offset with the time of the clock", func(ctx *specs.Context) {
			bg := context.TODO()
			const name, shard = "clock-stamp", uint64(2)
			clk := newManualClock()
			eventTimestamp := clk.Now().Add(time.Second).UnixNano()
			eventsStore, offsetStore := seededShard(ctx, name, shard, 0, eventTimestamp)

			runner := New(name, projection.NewDiscardHandler(), eventsStore, offsetStore, WithScope(runnerTestScope),
				WithPullInterval(interval), WithClock(clk))
			ctx.Expect(runner.Start(bg)).To(specs.BeNil())
			runner.Run(bg, nil)
			awaitTimer(ctx, clk)

			clk.Advance(interval)
			projectionID := &egopb.ProjectionId{ProjectionName: name, ShardNumber: shard}
			ctx.Eventually(offsetOf(offsetStore, projectionID), committedAt(eventTimestamp), poll...)

			offset, err := offsetStore.GetCurrentOffset(bg, projectionID)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(offset.GetTimestamp()).To(specs.Equal(clk.Now().UnixMilli()))

			ctx.Expect(runner.Stop()).To(specs.BeNil())
		})
	})
}
