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

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/command"
	"github.com/getsyntegrity/urd/egopb"
	testpb "github.com/getsyntegrity/urd/internal/testpb"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/testkit"
)

// -----------------------------------------------------------------------
// Regression coverage for the D9 batched-path defect found in adversarial
// review: a batch founded by a command with no ExpectedRevision (U) that
// later admits a command which DOES declare one (C) must still anchor the
// flush's single physical WriteEvents call to a real CAS precondition —
// batchHasPrecondition must become true the moment ANY admitted command in
// the cycle (founder or later) declares a revision, not only the founder,
// and batchBase must never move once the batch has opened. See
// openspec/changes/ego-write-004/design.md D9 and event_sourced_actor.go's
// batchBase/batchHasPrecondition field comment and processAndBatch.
//
// The harness below drives a precise, deterministic sequence of commands
// into the SAME open batch cycle. A batched command's Dispatch call blocks
// (the actor stashes it) until the eventual flush, so each step is fired
// from its own goroutine; the test does not move on to the next step until
// it has observed the current step's HandleCommand actually begin. Since a
// goakt actor processes its mailbox strictly one message to completion at a
// time, that observation proves the previous step's entire processAndBatch
// (admission gate, batchBase/batchHasPrecondition seeding, batchEntries
// append) has already finished running before the next step is even
// considered for admission — without relying on real-time sleeps.
// -----------------------------------------------------------------------

// batchStepBehavior wraps AccountEventSourcedBehavior, announcing the start
// of every HandleCommand call on entered so a test can synchronize on it.
type batchStepBehavior struct {
	*AccountEventSourcedBehavior
	entered chan Command
}

func newBatchStepBehavior(id string) *batchStepBehavior {
	return &batchStepBehavior{
		AccountEventSourcedBehavior: NewAccountEventSourcedBehavior(id),
		entered:                     make(chan Command),
	}
}

func (x *batchStepBehavior) HandleCommand(ctx context.Context, cmd Command, state State) ([]Event, error) {
	x.entered <- cmd
	return x.AccountEventSourcedBehavior.HandleCommand(ctx, cmd, state)
}

// batchStep is one command to dispatch, with its optional metadata (e.g. an
// ExpectedRevision declaration).
type batchStep struct {
	payload proto.Message
	opts    []command.MetadataOption
}

// stepU builds a step that declares no ExpectedRevision at all.
func stepU(payload proto.Message) batchStep {
	return batchStep{payload: payload}
}

// stepC builds a step that declares ExpectedRevision(revision).
func stepC(payload proto.Message, revision uint64) batchStep {
	return batchStep{payload: payload, opts: []command.MetadataOption{command.WithExpectedRevision(revision)}}
}

type batchDispatchOutcome struct {
	result command.Result
	err    error
}

// receiveG2 polls ch until a value is available and returns it. what names
// the awaited event in the failure message. The poll returns as soon as the
// value arrives; waitTimeout is only a ceiling.
func receiveG2[T any](ctx *specs.Context, ch <-chan T, what string) T {
	var got T
	received := false
	ctx.Eventually(func() any {
		select {
		case got = <-ch:
			received = true
		default:
		}
		return received
	}, specs.Satisfy(what, func(v any) bool { return v == true }),
		specs.WithTimeout(waitTimeout), specs.WithInterval(time.Millisecond))
	return got
}

// startBatchSteps dispatches each of steps, in strict order, into the same
// actor/batch cycle. It returns once the last step has reached HandleCommand,
// with one channel per step that will deliver that step's outcome. See the
// comment above batchStepBehavior for the synchronization argument.
func startBatchSteps(ctx *specs.Context, engine *Engine, entityID string, behavior *batchStepBehavior, steps []batchStep) []chan batchDispatchOutcome {
	chans := make([]chan batchDispatchOutcome, len(steps))
	for i, step := range steps {
		env := buildEnvelope(ctx, step.payload, step.opts...)

		ch := make(chan batchDispatchOutcome, 1)
		chans[i] = ch
		go func() {
			result, dispatchErr := engine.Dispatch(context.Background(), entityID, env, 15*time.Second)
			ch <- batchDispatchOutcome{result: result, err: dispatchErr}
		}()

		receiveG2(ctx, behavior.entered, "a step to reach HandleCommand")
	}
	return chans
}

// collectBatchResults waits for every step started by startBatchSteps and
// returns each step's command.Result in the same order.
func collectBatchResults(ctx *specs.Context, chans []chan batchDispatchOutcome) []command.Result {
	results := make([]command.Result, len(chans))
	for i, ch := range chans {
		out := receiveG2(ctx, ch, "a step to return a result")
		ctx.Expect(out.err).To(specs.BeNil())
		results[i] = out.result
	}
	return results
}

// runBatchSequence dispatches each of steps, in strict order, into the same
// actor/batch cycle and returns each step's command.Result in the same
// order.
func runBatchSequence(ctx *specs.Context, engine *Engine, entityID string, behavior *batchStepBehavior, steps []batchStep) []command.Result {
	return collectBatchResults(ctx, startBatchSteps(ctx, engine, entityID, behavior, steps))
}

// expectAllSucceededG2 asserts that every result succeeded.
func expectAllSucceededG2(ctx *specs.Context, results []command.Result) {
	outcomes := make([]command.Outcome, len(results))
	for i, result := range results {
		outcomes[i] = result.Outcome()
	}
	ctx.Expect(outcomes).To(specs.EveryElement(specs.Equal(command.OutcomeSuccess)))
}

// These tests flush through the event threshold or the admission gate. The
// production default also flushes after 5 ms, which can split their deliberately
// open batch before the next command arrives. A window longer than the test
// budget keeps that unrelated timer out of the sequence; shutdown cancels it.
// Assertions and command deadlines are unchanged.
//
// newBatchHarness spins up a fresh engine/entity pair wired through a
// preconditionSpyEventsStore (defined in event_sourced_actor_expected_revision_test.go)
// so the test can assert on the exact persistence.WritePrecondition each
// physical WriteEvents call actually received, and returns the pieces a test
// needs to drive it.
func newBatchHarness(ctx *specs.Context, name string, threshold int) (engine *Engine, entityID string, behavior *batchStepBehavior, spy *preconditionSpyEventsStore, store *testkit.EventStore) {
	underlying := connectedEventsStore(ctx)
	spy = &preconditionSpyEventsStore{EventStore: underlying}

	engine = startEngine(ctx, name, spy, WithLogger(DiscardLogger))

	entityID = uuid.NewString()
	behavior = newBatchStepBehavior(entityID)
	ctx.Expect(engine.Entity(context.Background(), behavior, WithBatchThreshold(threshold), WithBatchFlushWindow(time.Hour))).To(specs.BeNil())

	return engine, entityID, behavior, spy, underlying
}

// batchMatrixCase is one U/C admission sequence of the genesis matrix.
type batchMatrixCase struct {
	name  string
	build func(entityID string) []batchStep
	want  persistence.WritePrecondition
}

// TestBatchedPreconditionMatrix_GenesisBase exercises every U/C admission
// sequence design.md D9 names, on a brand-new aggregate (genesis base), and
// verifies the exact persistence.WritePrecondition that reaches the store's
// WriteEvents for the batch's one physical flush — not merely a boolean
// batchHasPrecondition inspection. U = no ExpectedRevision declared, C =
// ExpectedRevision declared and admitted. Each step produces exactly one
// event, so batchThreshold is set to len(steps) for each case: the batch
// stays open across every step and flushes exactly once, right after the
// last one.
func TestBatchedPreconditionMatrix_GenesisBase(t *testing.T) {
	tests := []batchMatrixCase{
		{
			name: "U_U",
			build: func(id string) []batchStep {
				return []batchStep{
					stepU(&testpb.CreateAccount{AccountBalance: 500}),
					stepU(&testpb.CreditAccount{AccountId: id, Balance: 10}),
				}
			},
			want: persistence.Unconditional(),
		},
		{
			name: "U_C",
			build: func(id string) []batchStep {
				return []batchStep{
					stepU(&testpb.CreateAccount{AccountBalance: 500}),
					stepC(&testpb.CreditAccount{AccountId: id, Balance: 10}, 1),
				}
			},
			want: persistence.ExpectGenesis(),
		},
		{
			name: "C_U",
			build: func(id string) []batchStep {
				return []batchStep{
					stepC(&testpb.CreateAccount{AccountBalance: 500}, 0),
					stepU(&testpb.CreditAccount{AccountId: id, Balance: 10}),
				}
			},
			want: persistence.ExpectGenesis(),
		},
		{
			name: "C_C",
			build: func(id string) []batchStep {
				return []batchStep{
					stepC(&testpb.CreateAccount{AccountBalance: 500}, 0),
					stepC(&testpb.CreditAccount{AccountId: id, Balance: 10}, 1),
				}
			},
			want: persistence.ExpectGenesis(),
		},
		{
			name: "U_U_C",
			build: func(id string) []batchStep {
				return []batchStep{
					stepU(&testpb.CreateAccount{AccountBalance: 500}),
					stepU(&testpb.CreditAccount{AccountId: id, Balance: 10}),
					stepC(&testpb.CreditAccount{AccountId: id, Balance: 10}, 2),
				}
			},
			want: persistence.ExpectGenesis(),
		},
		{
			name: "U_C_U",
			build: func(id string) []batchStep {
				return []batchStep{
					stepU(&testpb.CreateAccount{AccountBalance: 500}),
					stepC(&testpb.CreditAccount{AccountId: id, Balance: 10}, 1),
					stepU(&testpb.CreditAccount{AccountId: id, Balance: 10}),
				}
			},
			want: persistence.ExpectGenesis(),
		},
		{
			name: "C_U_C",
			build: func(id string) []batchStep {
				return []batchStep{
					stepC(&testpb.CreateAccount{AccountBalance: 500}, 0),
					stepU(&testpb.CreditAccount{AccountId: id, Balance: 10}),
					stepC(&testpb.CreditAccount{AccountId: id, Balance: 10}, 2),
				}
			},
			want: persistence.ExpectGenesis(),
		},
		{
			name: "C_C_C",
			build: func(id string) []batchStep {
				return []batchStep{
					stepC(&testpb.CreateAccount{AccountBalance: 500}, 0),
					stepC(&testpb.CreditAccount{AccountId: id, Balance: 10}, 1),
					stepC(&testpb.CreditAccount{AccountId: id, Balance: 10}, 2),
				}
			},
			want: persistence.ExpectGenesis(),
		},
		{
			// Multiple consecutive C's, beyond just three in a row.
			name: "C_C_C_C",
			build: func(id string) []batchStep {
				return []batchStep{
					stepC(&testpb.CreateAccount{AccountBalance: 500}, 0),
					stepC(&testpb.CreditAccount{AccountId: id, Balance: 10}, 1),
					stepC(&testpb.CreditAccount{AccountId: id, Balance: 10}, 2),
					stepC(&testpb.CreditAccount{AccountId: id, Balance: 10}, 3),
				}
			},
			want: persistence.ExpectGenesis(),
		},
	}

	specs.Describe(t, "a batch founded on a brand-new aggregate", func(s *specs.Spec) {
		specs.Table(s, tests, func(tc batchMatrixCase) string { return tc.name }, func(ctx *specs.Context, tc batchMatrixCase) {
			bg := context.Background()
			underlying := connectedEventsStore(ctx)
			spy := &preconditionSpyEventsStore{EventStore: underlying}

			// entityID must be known before steps are built (CreditAccount's
			// AccountId must match), so it is generated first and the entity is
			// spawned with it explicitly.
			entityID := uuid.NewString()
			steps := tc.build(entityID)

			engine := startEngine(ctx, "matrix-genesis-"+tc.name, spy, WithLogger(DiscardLogger))
			behavior := newBatchStepBehavior(entityID)
			ctx.Expect(engine.Entity(bg, behavior, WithBatchThreshold(len(steps)), WithBatchFlushWindow(time.Hour))).To(specs.BeNil())

			results := runBatchSequence(ctx, engine, entityID, behavior, steps)
			expectAllSucceededG2(ctx, results)

			// Exactly one physical flush for the whole batch, with the expected
			// precondition.
			ctx.Expect(spy.recorded()).ToEqual([]persistence.WritePrecondition{tc.want})

			// Every step's event actually committed.
			event, err := underlying.GetLatestEvent(bg, persistence.Unscoped(), entityID)
			ctx.Expect(err).To(specs.BeNil())
			specs.ExpectT(ctx, event.GetSequenceNumber()).ToEqual(uint64(len(steps)))
		})
	})
}

// TestBatchedPhysicalBaseAnchorsToPreBatchRevision_NotLogicalCounter is the
// critical Step 3 test: it proves the batch's physical CAS is anchored to
// the store revision that existed the moment the batch opened (batchBase),
// never to the running logical batchCounter a later admitted command's own
// declared revision advances to.
//
// Persistence starts at revision R (=2, established by an earlier,
// already-flushed batch). A fresh batch then opens: C1 (=U here) founds it
// unconditionally (batchBase seeded to eventsCounter==R==2); C2 (=C) is
// admitted declaring ExpectedRevision == batchCounter (3, the revision the
// batch reaches immediately before C2's own event) — matching design.md
// D9's admission rule. The flush must still check storage against R (2),
// never against 3 (C2's own declared value) nor 4 (the batch's logical end
// revision after both commands).
func TestBatchedPhysicalBaseAnchorsToPreBatchRevision_NotLogicalCounter(t *testing.T) {
	specs.Describe(t, "a batch opened on top of an already persisted revision", func(s *specs.Spec) {
		s.It("anchors its physical CAS to the pre-batch revision, not the logical counter", func(ctx *specs.Context) {
			bg := context.Background()
			engine, entityID, behavior, spy, store := newBatchHarness(ctx, "physical-base-vs-logical", 2)

			// Prefix phase: two unconditional commands, in their own batch cycle
			// (batchThreshold==2 auto-flushes right after them), to establish a
			// real, already-confirmed, non-zero storage revision R=2.
			prefix := []batchStep{
				stepU(&testpb.CreateAccount{AccountBalance: 500}),
				stepU(&testpb.CreditAccount{AccountId: entityID, Balance: 10}),
			}
			expectAllSucceededG2(ctx, runBatchSequence(ctx, engine, entityID, behavior, prefix))
			ctx.Expect(spy.recorded()).ToEqual([]persistence.WritePrecondition{persistence.Unconditional()})

			// R=2 after the prefix batch's flush.
			event, err := store.GetLatestEvent(bg, persistence.Unscoped(), entityID)
			ctx.Expect(err).To(specs.BeNil())
			specs.ExpectT(ctx, event.GetSequenceNumber()).ToEqual(2)

			// Batch-under-test: U founds (batchBase seeded to eventsCounter==2),
			// C is admitted declaring ExpectedRevision(3) (batchCounter after U's
			// own event). The physical CAS must use base 2, not 3, and not the
			// batch's logical end revision (4).
			underTest := []batchStep{
				stepU(&testpb.CreditAccount{AccountId: entityID, Balance: 10}),
				stepC(&testpb.CreditAccount{AccountId: entityID, Balance: 10}, 3),
			}
			expectAllSucceededG2(ctx, runBatchSequence(ctx, engine, entityID, behavior, underTest))

			recorded := spy.recorded()
			ctx.Expect(recorded).To(specs.HaveLen(2))
			got := recorded[1]
			// Must anchor to the PRE-BATCH physical base (R=2), not the logical
			// mid/end-of-batch revision.
			ctx.Expect(got).ToEqual(persistence.ExpectRevision(2))
			ctx.Expect(got).To(specs.Not(specs.BeOneOf(persistence.ExpectRevision(3), persistence.ExpectRevision(4))))

			// Both batches' events are committed: 2 (prefix) + 2 (under test).
			event, err = store.GetLatestEvent(bg, persistence.Unscoped(), entityID)
			ctx.Expect(err).To(specs.BeNil())
			specs.ExpectT(ctx, event.GetSequenceNumber()).ToEqual(4)
		})
	})
}

// TestBatchedZeroEventAdmittedCommandStillPreservesLaterPrecondition is
// Step 5's core case: a command admitted into an open batch with a matching
// declared ExpectedRevision, but whose handler produces zero events (an
// idempotent no-op via testpb.TestNoEvent), must still cause
// batchHasPrecondition to become true — it must not be silently lost merely
// because that particular command happened to persist nothing of its own.
func TestBatchedZeroEventAdmittedCommandStillPreservesLaterPrecondition(t *testing.T) {
	specs.Describe(t, "an admitted command whose handler produces zero events", func(s *specs.Spec) {
		s.It("still makes the flush a conditional write", func(ctx *specs.Context) {
			bg := context.Background()
			// threshold=2: only the two *event-producing* commands (U founder, U
			// filler) count toward it; the zero-event C in between contributes
			// nothing to batchNumEvents, so it does not itself trigger a flush.
			engine, entityID, behavior, spy, store := newBatchHarness(ctx, "zero-event-preserves-precondition", 2)

			// testpb.TestNoEvent is the zero-event command AccountEventSourcedBehavior
			// recognizes (HandleCommand returns nil, nil for it).
			realSteps := []batchStep{
				stepU(&testpb.CreateAccount{AccountBalance: 500}),
				stepC(&testpb.TestNoEvent{}, 1),                                // admitted (1 == batchCounter), produces 0 events
				stepU(&testpb.CreditAccount{AccountId: entityID, Balance: 10}), // filler: reaches threshold, forces the flush
			}

			results := runBatchSequence(ctx, engine, entityID, behavior, realSteps)
			expectAllSucceededG2(ctx, results)

			// The zero-event step's own reply must report the unchanged revision
			// (1, from the founder alone) — it never advanced batchCounter itself.
			specs.ExpectT(ctx, results[1].Revision()).ToEqual(1)

			// Exactly one physical flush, and the zero-event command's declared
			// ExpectedRevision(1) is still honored as a real CAS precondition.
			ctx.Expect(spy.recorded()).ToEqual([]persistence.WritePrecondition{persistence.ExpectGenesis()})

			// Only the two real events (founder + filler) were ever persisted.
			event, err := store.GetLatestEvent(bg, persistence.Unscoped(), entityID)
			ctx.Expect(err).To(specs.BeNil())
			specs.ExpectT(ctx, event.GetSequenceNumber()).ToEqual(2)
		})
	})
}

// TestBatchedZeroEventFounderNeverOpensBatch documents the companion case:
// a command that would otherwise found a brand-new batch, but itself
// produces zero events, never actually opens one — batchBase/batchHasPrecondition
// stay untouched, and the very next (event-producing) command becomes the
// real founder instead.
func TestBatchedZeroEventFounderNeverOpensBatch(t *testing.T) {
	specs.Describe(t, "a would-be batch founder whose handler produces zero events", func(s *specs.Spec) {
		s.It("never opens a batch, so the next command becomes the real founder", func(ctx *specs.Context) {
			bg := context.Background()
			// threshold=1: the real founder (the second command here) auto-flushes
			// on its own, since it is alone in its batch cycle.
			engine, entityID, behavior, spy, store := newBatchHarness(ctx, "zero-event-founder-noop", 1)

			steps := []batchStep{
				stepC(&testpb.TestNoEvent{}, 0),                   // would-be founder; produces 0 events; batch never opens
				stepU(&testpb.CreateAccount{AccountBalance: 500}), // the real founder
			}
			results := runBatchSequence(ctx, engine, entityID, behavior, steps)
			expectAllSucceededG2(ctx, results)

			// The zero-event command must not have anchored a genesis batchBase.
			ctx.Expect(spy.recorded()).ToEqual([]persistence.WritePrecondition{persistence.Unconditional()})

			// Only the real founder's single event was ever persisted.
			event, err := store.GetLatestEvent(bg, persistence.Unscoped(), entityID)
			ctx.Expect(err).To(specs.BeNil())
			specs.ExpectT(ctx, event.GetSequenceNumber()).ToEqual(1)
		})
	})
}

// TestBatchAdmissionGateRejectsStaleRevision_ForcesEarlyFlushThenFoundsFreshBatch
// covers Step 6's stale/mismatched-revision edge: a command declaring an
// ExpectedRevision that does not match the currently open batch's counter is
// never admitted into it. Instead (design.md D9 step 4) it forces an early
// flush of what is already staged, then founds a brand-new batch on its own
// — where its own stale declared revision is still caught by the store's
// real CAS at that new batch's own flush, not silently ignored.
func TestBatchAdmissionGateRejectsStaleRevision_ForcesEarlyFlushThenFoundsFreshBatch(t *testing.T) {
	specs.Describe(t, "a command declaring a revision that does not match the open batch", func(s *specs.Spec) {
		s.It("forces an early flush, then founds a fresh batch that the store rejects", func(ctx *specs.Context) {
			bg := context.Background()
			engine, entityID, behavior, spy, store := newBatchHarness(ctx, "stale-revision-admission", 2)

			steps := []batchStep{
				stepU(&testpb.CreateAccount{AccountBalance: 500}),                  // founder, batch stays open (threshold=2, 1 event so far)
				stepC(&testpb.CreditAccount{AccountId: entityID, Balance: 10}, 99), // stale: batchCounter is 1, not 99
				stepU(&testpb.CreditAccount{AccountId: entityID, Balance: 10}),     // filler for the fresh batch the stale command founds
			}
			results := runBatchSequence(ctx, engine, entityID, behavior, steps)

			// Step 0 (U, the original founder) is confirmed by the forced early
			// flush (Unconditional, since nothing had declared a revision yet).
			ctx.Expect(results[0].Outcome()).ToEqual(command.OutcomeSuccess)

			// Steps 1 and 2 land in a second batch founded by the stale command
			// itself (batchBase=99, its own declared value); that batch's flush
			// checks storage (real revision 1) against 99 and must conflict.
			for _, result := range results[1:] {
				expectConcurrencyConflict(ctx, result)
			}

			// The forced early flush plus the stale command's own fresh-batch
			// flush. The first one is the forced flush of the original open batch
			// (nothing in it had declared a revision); the second anchors to the
			// stale command's own declared revision.
			ctx.Expect(spy.recorded()).ToEqual([]persistence.WritePrecondition{
				persistence.Unconditional(),
				persistence.ExpectRevision(99),
			})

			// Only the original founder's event ever committed; the stale-founded
			// batch's conflict must not have persisted anything.
			event, err := store.GetLatestEvent(bg, persistence.Unscoped(), entityID)
			ctx.Expect(err).To(specs.BeNil())
			specs.ExpectT(ctx, event.GetSequenceNumber()).ToEqual(1)
		})
	})
}

// TestBatchedExternalWriterWinsCAS_RejectsWholeBatchWithoutAdvancingCounter
// is Step 4's mandatory adversarial test: an independent writer advances
// persistence, behind the actor's back, after a batch has already admitted
// a command declaring an ExpectedRevision but before that batch physically
// flushes. The flush's real CAS (exercised through the actual
// persistence/testkit store, not merely batchHasPrecondition inspection)
// must fail, every command in the batch must surface OutcomeRejected with
// Failure.Code()==CodeConcurrencyConflict, no rejected event may be
// confirmed, and eventsCounter must not advance (design.md D9/D10).
func TestBatchedExternalWriterWinsCAS_RejectsWholeBatchWithoutAdvancingCounter(t *testing.T) {
	specs.Describe(t, "an external writer that advances the stream before the batch flushes", func(s *specs.Spec) {
		s.It("rejects the whole batch and keeps only the external writer's event", func(ctx *specs.Context) {
			bg := context.Background()
			// threshold=3: after the two official commands (U founder + C, 2
			// events), the batch stays open — giving the test a window to act as
			// an external writer before the third (filler) command tips it over
			// the threshold and forces the flush.
			engine, entityID, behavior, spy, store := newBatchHarness(ctx, "external-writer-cas-conflict", 3)

			official := []batchStep{
				stepU(&testpb.CreateAccount{AccountBalance: 500}),                 // founder: batchBase=0 (genesis)
				stepC(&testpb.CreditAccount{AccountId: entityID, Balance: 10}, 1), // admitted: batchHasPrecondition=true
			}
			officialChans := startBatchSteps(ctx, engine, entityID, behavior, official)

			// At this point both official commands are staged (2 events, threshold
			// 3 not yet reached) and the actor is idle, waiting on its mailbox.
			// Act as an independent external writer: commit an event directly to
			// the SAME persistence-id's stream, bypassing the actor entirely, which
			// physically advances the store to revision 1 while the batch's
			// anchored batchBase (genesis, 0) and the actor's own eventsCounter
			// (still 0, nothing confirmed yet) know nothing about it.
			externalEventAny, err := anypb.New(&testpb.AccountCreated{AccountId: entityID, AccountBalance: 999})
			ctx.Expect(err).To(specs.BeNil())
			externalEvent := &egopb.Event{
				PersistenceId:  entityID,
				SequenceNumber: 1,
				Event:          externalEventAny,
				Timestamp:      time.Now().Unix(),
				Shard:          0,
			}
			ctx.Expect(store.WriteEvents(bg, persistence.Unscoped(), []*egopb.Event{externalEvent}, persistence.Unconditional())).To(specs.BeNil())

			// Now dispatch the filler command that tips batchNumEvents over the
			// threshold, forcing the flush.
			filler := []batchStep{stepU(&testpb.CreditAccount{AccountId: entityID, Balance: 10})}
			fillerChans := startBatchSteps(ctx, engine, entityID, behavior, filler)

			results := collectBatchResults(ctx, append(officialChans, fillerChans...))

			for _, result := range results {
				expectConcurrencyConflict(ctx, result)
				conflict := conflictError(ctx, result)
				ctx.Expect(conflict.Expected()).ToEqual(persistence.ExpectGenesis())
				// The actual revision is the external writer's event.
				actual, ok := conflict.ActualRevision()
				ctx.Expect(ok).To(specs.BeTrue())
				specs.ExpectT(ctx, actual).ToEqual(1)
			}

			ctx.Expect(spy.recorded()).ToEqual([]persistence.WritePrecondition{persistence.ExpectGenesis()})

			// D10 recheck: no rejected event was confirmed, and eventsCounter must
			// not have advanced as a result of the rejected persist — only the
			// external writer's single event is visible in the store.
			event, err := store.GetLatestEvent(bg, persistence.Unscoped(), entityID)
			ctx.Expect(err).To(specs.BeNil())
			specs.ExpectT(ctx, event.GetSequenceNumber()).ToEqual(1)

			// The visible event is the external writer's own, not one of the
			// rejected batch's.
			var committed testpb.AccountCreated
			ctx.Expect(event.GetEvent().UnmarshalTo(&committed)).To(specs.BeNil())
			specs.ExpectT(ctx, committed.GetAccountBalance()).ToEqual(999)
		})
	})
}
