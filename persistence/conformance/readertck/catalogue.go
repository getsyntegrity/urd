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

package readertck

import (
	"fmt"
	"math/rand/v2"

	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
)

// Scenario names. Tests and the documentation refer to them.
const (
	ScenarioIssueCase        = "late-commit-issue-case"
	ScenarioIDOrderLate      = "late-commit-lower-id-higher-timestamp"
	ScenarioLongTransaction  = "long-transaction-bounded"
	ScenarioAbortReleases    = "aborted-transaction-releases-the-reader"
	ScenarioTieBatchCut      = "batch-limit-inside-a-tie-group"
	ScenarioTieLateCommit    = "tie-group-completed-by-a-later-commit"
	ScenarioRetry            = "retry-redelivers-with-a-stable-signal"
	ScenarioRestartResume    = "restart-resumes-from-the-persisted-cursor"
	ScenarioRestartUnpersist = "restart-before-the-cursor-was-persisted"
	ScenarioScopeSelection   = "selection-by-scope"
	ScenarioRejection        = "selection-rejection"
	ScenarioSliceRange       = "selection-by-slice-range"
	ScenarioConditionsBroken = "eligibility-conditions-broken"
)

var (
	scopeUnscoped = persistence.Unscoped()
	scopeA        = mustScope("tenant-a")
	scopeB        = mustScope("tenant-b")
)

func mustScope(id string) persistence.Scope {
	s, err := persistence.NewTenantScope(tenancy.TenantID(id))
	if err != nil {
		panic(err)
	}
	return s
}

func ev(label string, scope persistence.Scope, pid string, slice uint32, ts int64) Event {
	return Event{Label: label, Scope: scope, PersistenceID: pid, Seq: 1, Slice: slice, Timestamp: ts}
}

func unscopedConsumer() Consumer {
	return Consumer{Name: "c", Selection: OneScope(scopeUnscoped), Slices: AllSlices()}
}

var allConditions = Conditions{
	DedicatedCluster: true, WriterTimeoutAllRoles: true, NoPreparedTransactions: true, OldTransactionAlert: true,
}

// Catalogue returns the fixed scenarios, in a stable order. Each one is plain
// data: a seed-free script of writer transactions, clock ticks and polls.
func Catalogue() []Scenario {
	one := []Consumer{unscopedConsumer()}
	return []Scenario{
		{
			Name: ScenarioIssueCase,
			Doc: "a@100 and b@200 commit; late@150, appended earlier by a concurrent transaction, commits after a reader " +
				"already passed timestamp 200. It must still be delivered. This is the case of #348.",
			Asserts: Safety | Eligibility | Progress, Conditions: allConditions, LMax: 5, T: 5,
			Consumers: one,
			Steps: []Step{
				Begin("tx-late"), Append("tx-late", ev("late@150", scopeUnscoped, "late", 1, 150)),
				Begin("tx-ab"), Append("tx-ab", ev("a@100", scopeUnscoped, "a", 1, 100)),
				Append("tx-ab", ev("b@200", scopeUnscoped, "b", 1, 200)), Commit("tx-ab"),
				Tick(1), Drain("c", 2),
				Tick(1), Commit("tx-late"),
				Tick(3), Drain("c", 2), // a and b are due (commit tick 0 + T 5)
				Tick(2), Drain("c", 2), // late is due (commit tick 2 + T 5)
				Tick(10), Drain("c", 2),
			},
		},
		{
			Name: ScenarioIDOrderLate,
			Doc: "x is appended first (lowest insertion id) with the highest timestamp, y second with a low one; y commits " +
				"and is read, then x commits. A cursor on insertion id or on timestamp alone loses one of them in some " +
				"variant of this shape; either must deliver x.",
			Asserts: Safety | Progress, Consumers: one,
			Steps: []Step{
				Begin("t1"), Append("t1", ev("x@300", scopeUnscoped, "x", 1, 300)),
				Begin("t2"), Append("t2", ev("y@100", scopeUnscoped, "y", 1, 100)), Commit("t2"),
				Drain("c", 1), Commit("t1"), Drain("c", 1),
			},
		},
		{
			Name: ScenarioLongTransaction,
			Doc: "one writer holds a transaction open for LMax ticks while two others commit. Nothing is promised while it " +
				"is open (events may be withheld); once it ends everything becomes visible within T of its own commit.",
			Asserts: Safety | Eligibility | Progress, Conditions: allConditions, LMax: 10, T: 10,
			Consumers: one,
			Steps: []Step{
				Begin("long"), Append("long", ev("z@50", scopeUnscoped, "z", 1, 50)),
				Tick(1), Begin("t2"), Append("t2", ev("e1@60", scopeUnscoped, "e1", 1, 60)), Commit("t2"),
				Tick(1), Begin("t3"), Append("t3", ev("e2@70", scopeUnscoped, "e2", 1, 70)), Commit("t3"),
				Drain("c", 2),
				Tick(8), Commit("long"),
				Tick(1), Drain("c", 2), // e1 due (1+10)
				Tick(1), Drain("c", 2), // e2 due (2+10)
				Tick(8), Drain("c", 2), // z due (10+10)
			},
		},
		{
			Name: ScenarioAbortReleases,
			Doc: "a writer that rolls back must not hold the reader back: once it resolves, the events committed behind it " +
				"are delivered, and the aborted one never is.",
			Asserts: Safety | Progress, Consumers: one,
			Steps: []Step{
				Begin("t1"), Append("t1", ev("lost@10", scopeUnscoped, "lost", 1, 10)),
				Begin("t2"), Append("t2", ev("y@20", scopeUnscoped, "y", 1, 20)), Commit("t2"),
				Drain("c", 2), Abort("t1"), Drain("c", 2),
			},
		},
		{
			Name: ScenarioTieBatchCut,
			Doc: "five events share the ordering key (one timestamp); the batch limit is 2. Whatever the cursor is, stopping " +
				"inside the tie group must not lose the rest of it.",
			Asserts: Safety | Progress, Consumers: one,
			Steps: []Step{
				Begin("t"),
				Append("t", ev("p1", scopeUnscoped, "p1", 1, 100)), Append("t", ev("p2", scopeUnscoped, "p2", 1, 100)),
				Append("t", ev("p3", scopeUnscoped, "p3", 1, 100)), Append("t", ev("p4", scopeUnscoped, "p4", 1, 100)),
				Append("t", ev("p5", scopeUnscoped, "p5", 1, 100)), Commit("t"),
				Poll("c", 2), Poll("c", 2), Poll("c", 2), Drain("c", 2),
			},
		},
		{
			Name: ScenarioTieLateCommit,
			Doc: "an event with the same ordering key as one already delivered commits afterwards (and sorts before it by " +
				"persistence id). A reader that treats 'strictly after the last key' as done loses it.",
			Asserts: Safety | Progress, Consumers: one,
			Steps: []Step{
				Begin("t1"), Append("t1", ev("q1", scopeUnscoped, "q1", 1, 100)), Commit("t1"),
				Poll("c", 5),
				Begin("t2"), Append("t2", ev("q0", scopeUnscoped, "q0", 1, 100)), Commit("t2"),
				Drain("c", 5),
			},
		},
		{
			Name: ScenarioRetry,
			Doc: "the consumer crashes after a batch and before persisting its cursor, twice, so the same batch is read " +
				"again. At-least-once delivery is allowed; the idempotence signal must be stable and unique.",
			Asserts: Safety | Progress, Consumers: one,
			Steps: []Step{
				Begin("t"),
				Append("t", ev("r1", scopeUnscoped, "r1", 1, 10)), Append("t", ev("r2", scopeUnscoped, "r2", 1, 20)),
				Append("t", ev("r3", scopeUnscoped, "r3", 1, 30)), Append("t", ev("r4", scopeUnscoped, "r4", 1, 40)),
				Commit("t"),
				PollUnpersisted("c", 2), PollUnpersisted("c", 2), Poll("c", 2), Drain("c", 2),
			},
		},
		{
			Name: ScenarioRestartResume,
			Doc: "the reader restarts twice from its persisted cursor while events keep committing; nothing may be skipped " +
				"and the restart must not move the position.",
			Asserts: Safety | Progress, Consumers: one,
			Steps: []Step{
				Begin("t1"),
				Append("t1", ev("s1", scopeUnscoped, "s1", 1, 10)), Append("t1", ev("s2", scopeUnscoped, "s2", 1, 20)),
				Append("t1", ev("s3", scopeUnscoped, "s3", 1, 30)), Append("t1", ev("s4", scopeUnscoped, "s4", 1, 40)),
				Append("t1", ev("s5", scopeUnscoped, "s5", 1, 50)), Append("t1", ev("s6", scopeUnscoped, "s6", 1, 60)),
				Commit("t1"),
				Poll("c", 2), Restart(), Poll("c", 2),
				Begin("t2"), Append("t2", ev("s7", scopeUnscoped, "s7", 1, 70)), Commit("t2"),
				Restart(), Drain("c", 2),
			},
		},
		{
			Name: ScenarioRestartUnpersist,
			Doc: "the process dies after a batch was delivered but before its cursor was persisted; the new instance resumes " +
				"from the older cursor and may redeliver, but must not skip.",
			Asserts: Safety | Progress, Consumers: one,
			Steps: []Step{
				Begin("t"),
				Append("t", ev("u1", scopeUnscoped, "u1", 1, 10)), Append("t", ev("u2", scopeUnscoped, "u2", 1, 20)),
				Append("t", ev("u3", scopeUnscoped, "u3", 1, 30)), Append("t", ev("u4", scopeUnscoped, "u4", 1, 40)),
				Commit("t"),
				PollUnpersisted("c", 3), Restart(), Drain("c", 3),
			},
		},
		{
			Name: ScenarioScopeSelection,
			Doc: "OneScope(Unscoped()) returns only unscoped events and is not a wildcard; OneScope(tenant) only that " +
				"tenant's; the privileged AllScopesInCell returns all of them. A late commit lands in the middle.",
			Asserts: Safety | Progress,
			Consumers: []Consumer{
				{Name: "unscoped", Selection: OneScope(scopeUnscoped), Slices: AllSlices()},
				{Name: "tenant-a", Selection: OneScope(scopeA), Slices: AllSlices()},
				{Name: "cell", Selection: AllScopesInCell(), Slices: AllSlices(), Privileged: true},
			},
			Steps: []Step{
				Begin("late"), Append("late", ev("a3", scopeA, "a3", 1, 5)),
				Begin("t"),
				Append("t", ev("u1", scopeUnscoped, "u1", 1, 10)), Append("t", ev("a1", scopeA, "a1", 1, 20)),
				Append("t", ev("b1", scopeB, "b1", 1, 30)), Append("t", ev("u2", scopeUnscoped, "u2", 1, 40)),
				Commit("t"),
				Drain("unscoped", 2), Drain("tenant-a", 2), Drain("cell", 2),
				Commit("late"),
				Drain("unscoped", 2), Drain("tenant-a", 2), Drain("cell", 2),
			},
		},
		{
			Name: ScenarioRejection,
			Doc: "the wildcard without the privilege, and a selection built on the invalid zero Scope, are refused and " +
				"deliver nothing; the valid single-tenant read next to them still works.",
			Asserts: Safety | Progress,
			Consumers: []Consumer{
				{Name: "cell-unprivileged", Selection: AllScopesInCell(), Slices: AllSlices(), ExpectReject: true},
				{Name: "zero-scope", Selection: OneScope(persistence.Scope{}), Slices: AllSlices(), ExpectReject: true},
				{Name: "unscoped", Selection: OneScope(scopeUnscoped), Slices: AllSlices()},
			},
			Steps: []Step{
				Begin("t"), Append("t", ev("u1", scopeUnscoped, "u1", 1, 10)), Append("t", ev("a1", scopeA, "a1", 1, 20)),
				Commit("t"),
				Poll("cell-unprivileged", 5), Poll("zero-scope", 5), Drain("unscoped", 5),
			},
		},
		{
			Name: ScenarioSliceRange,
			Doc: "a consumer of slices 2..3 gets exactly the events of those slices, including one that commits late; a " +
				"second consumer reads every slice.",
			Asserts: Safety | Progress,
			Consumers: []Consumer{
				{Name: "mid", Selection: OneScope(scopeUnscoped), Slices: SliceRange{Lo: 2, Hi: 3}},
				{Name: "all", Selection: OneScope(scopeUnscoped), Slices: AllSlices()},
			},
			Steps: []Step{
				Begin("late"), Append("late", ev("m3", scopeUnscoped, "m3", 3, 5)),
				Begin("t"),
				Append("t", ev("m1", scopeUnscoped, "m1", 1, 10)), Append("t", ev("m2", scopeUnscoped, "m2", 2, 20)),
				Append("t", ev("m4", scopeUnscoped, "m4", 4, 30)), Commit("t"),
				Drain("mid", 1), Drain("all", 1),
				Commit("late"),
				Drain("mid", 1), Drain("all", 1),
			},
		},
		{
			Name: ScenarioConditionsBroken,
			Doc: "a prepared-style transaction stays open far beyond LMax because the operational conditions do not hold " +
				"(no guard on prepared transactions, no alert). Bounded eligibility is NOT claimed; safety and progress " +
				"still are.",
			Asserts:    Safety | Eligibility | Progress,
			Conditions: Conditions{DedicatedCluster: true, WriterTimeoutAllRoles: true},
			LMax:       5, T: 5, Consumers: one,
			Steps: []Step{
				Begin("prepared"), Append("prepared", ev("held@10", scopeUnscoped, "held", 1, 10)),
				Begin("t2"), Append("t2", ev("e1@20", scopeUnscoped, "e1", 1, 20)), Commit("t2"),
				Tick(6), Drain("c", 2),
				Tick(44), Commit("prepared"),
				Drain("c", 2),
			},
		},
	}
}

// Generate builds a randomised scenario from seed. The same seed always yields
// the same scenario: interleavings of writer transactions (some aborting),
// timestamp ties, polls with small limits and restarts, ending in a drain. It
// asserts safety and progress only.
func Generate(seed uint64) Scenario {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	nTx := 2 + rng.IntN(3)
	type plan struct {
		id      string
		actions []Step
	}
	plans := make([]plan, nTx)
	for k := range plans {
		id := fmt.Sprintf("g%d", k)
		acts := []Step{Begin(id)}
		for j, n := 0, 1+rng.IntN(3); j < n; j++ {
			pid := fmt.Sprintf("g%d-%d", k, j)
			acts = append(acts, Append(id, ev(pid, scopeUnscoped, pid, 1, int64(100+10*rng.IntN(4)))))
		}
		if rng.IntN(5) == 0 {
			acts = append(acts, Abort(id))
		} else {
			acts = append(acts, Commit(id))
		}
		plans[k] = plan{id: id, actions: acts}
	}
	var steps []Step
	for remaining := nTx; remaining > 0; {
		switch rng.IntN(6) {
		case 0:
			steps = append(steps, Poll("c", 1+rng.IntN(3)))
		case 1:
			steps = append(steps, Tick(1))
		case 2:
			if rng.IntN(2) == 0 {
				steps = append(steps, Restart())
			}
		default:
			k := rng.IntN(nTx)
			if len(plans[k].actions) == 0 {
				continue
			}
			steps = append(steps, plans[k].actions[0])
			plans[k].actions = plans[k].actions[1:]
			if len(plans[k].actions) == 0 {
				remaining--
			}
		}
	}
	steps = append(steps, Drain("c", 1+rng.IntN(3)))
	return Scenario{
		Name:      fmt.Sprintf("generated-seed-%d", seed),
		Doc:       "randomised interleaving from a seed; see Generate",
		Seed:      seed,
		Asserts:   Safety | Progress,
		Consumers: []Consumer{unscopedConsumer()},
		Steps:     steps,
	}
}
