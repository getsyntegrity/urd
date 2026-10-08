# Reader conformance: specification, harness and oracle (#348, I-02)

| Field | Value |
|---|---|
| Status | **Draft specification and provisional harness.** Not an approved contract. |
| Tracker | #348 (I-02), epic #342, phase 0. The contract that this oracle will test is #351 (I-05); the mechanism comparison is #352 / Gate A. |
| Code | `persistence/conformance/readertck` (location approved in D1: outside `internal` so that the separate `inttest` module can import it, minimal API for tests, no dependency from production code; no compatibility promise until the reader SPI is approved). The PostgreSQL run is `inttest/flows/reader`. |
| Not decided here | The read mechanism, the number of slices N, the hash, the key or cursor encoding, the public reader API. PR #436 (provisional `SliceOf`) is untouched. |

Three words are used with a fixed meaning: **specified** (this document says what must hold), **executed** (a unit test in this change runs it today, with fakes, no database), **pending** (cannot run until a real adapter or an approved SPI exists).

## 1. Scope and model

The harness evaluates any mechanism that reads confirmed events of a journal in batches with a persisted opaque cursor. It is written against two small test-local interfaces, `Subject` (`Poll(Request) (Result, error)`) and `Backend` (writer transactions, a logical clock, a factory of reader instances). They are placeholders for the SPI that #351 has not approved. Nothing is exported from `persistence/conformance`.

- **Writers** run transactions: begin, append events, commit or abort. An event is *confirmed* when its transaction commits. The writer stamps a timestamp before it commits, so timestamp order, insertion order and commit order may all differ.
- **Consumer** = a selection (`OneScope(scope)` or `AllScopesInCell`, the latter privileged), an inclusive slice range, and its own persisted cursor. The cursor is opaque bytes: the harness stores it and hands it back, never parses it.
- **Handler never fails.** The consumer persists the returned cursor after every batch, except in the steps that model a crash before persisting. Runner error handling is I-22.
- **Time is a logical tick counter** moved by the script. No wall clock, no sleeps; randomised scenarios derive from a seed.

## 2. The three properties

They are judged separately. A reader can be safe and slow (stall mutant), or fast and unsafe (skip mutants).

### 2.1 Safety

> Every confirmed event matching the consumer's selection is eventually delivered, and nothing wrong is ever delivered.

Judged once every writer has resolved, over the whole run, so it is independent of timing. Rules (violation codes in `oracle.go`):

| Rule | Code |
|---|---|
| Zero omissions: a confirmed event is never left behind the cursor | `omission` |
| No uncommitted or aborted event is delivered | `phantom` |
| Nothing outside the selection: another scope, another slice | `wrong-selection` |
| Nothing that nobody wrote | `unknown-event` |
| Per persistence id, first deliveries arrive in sequence order | `order` |
| Redelivery is allowed (at-least-once) but needs a stable, unique idempotence token, and no event twice in one batch | `dup-token`, `missing-token`, `dup-in-batch` |
| `AllScopesInCell` without privilege and a selection on the zero `Scope` are refused and deliver nothing | `not-rejected` |
| A restart resumes exactly from the persisted cursor | via `omission` |

**Necessary conditions** (what any mechanism must ensure, stated without choosing one):

1. S1. The reader never moves its position past an event that may still commit and belong behind that position, unless the cursor keeps enough information to deliver that event later. A cursor that is only a high-water mark of the writers' timestamps or of insertion ids does not satisfy this on its own.
2. S2. A batch boundary may fall inside a group of events with equal ordering key only if the cursor can resume inside the group; otherwise the batch must extend to the end of the group.
3. S3. The selection (scope, and slice range) is applied by the reader and bound to the cursor's meaning; `OneScope(Unscoped())` is a valid single-tenant read that never returns a tenant's events, and is not the wildcard.
4. S4. Delivery is at-least-once and every delivery carries a signal that lets the consumer discard a duplicate.
5. S5. A restart, or a crash before the cursor was persisted, never moves the position forward.

**Explicitly not required**: a global delivery order on the ordering key (a late commit legitimately arrives after later timestamps); absence of duplicates; a hard cap of the batch size at `limit` (today's contract treats `limit` as a minimum-batch target, which maintainers may keep or change, see section 7).

### 2.2 Eligibility

> When does a confirmed event become visible to the reader. This is about the reader seeing it, **not** about the application of it by a handler: no check here promises application latency.

A bounded claim has the form: *an event committed at tick c is delivered by the first drain at tick >= c + T*. The harness judges it only when `Scenario.Conditions.All()` holds, and `Validate` refuses a scenario that claims the bound while one of its transactions lasts longer than `LMax` (`T >= LMax` is required).

A mechanism that holds the reader behind the oldest unresolved writer transaction (a transaction-id horizon such as `xid8` is the typical example; it is named as an example and **not chosen**) makes visibility depend on writers closing. The bound `T = LMax` is true **only** when every writer transaction ends within `LMax`, which needs all four operational conditions:

| Condition | Why the bound needs it |
|---|---|
| Dedicated cluster | Nothing foreign can hold a transaction open on the same database |
| A timeout for ALL roles that write, not only the application's | One role without a timeout can hold the horizon back forever |
| No prepared transactions | A timeout does not bound a prepared transaction |
| An alert on old transaction ids | It is how a violation of the other three is noticed |

When any of them fails: **safety must still hold** (the reader may withhold events, never skip them), **bounded eligibility is not claimed** (`NotClaimed`, not `Fail`), and progress is judged from the moment the writer resolves. Scenario `eligibility-conditions-broken` runs this, and a test shows the same trace would fail eligibility if the bound were claimed anyway.

A mechanism that never holds events back (for example a cursor that records delivered keys) has no such dependency on writers; it has other costs, which is #352's comparison, not this document's.

### 2.3 Progress

> Once no writer transaction is open (and `Settle` ticks have passed), the reader moves: draining reaches every confirmed event in a bounded number of polls.

Judged on the scripted `Drain` steps that start quiescent. A poll that brings no new event counts against `EmptyPollBudget` (2): the cursor may take a poll or two to step over aborted or filtered ranges, but not more. Violations: `stall` (budget exceeded with events pending), `no-convergence` (the drain never settled). While a writer is open nothing is demanded: withholding is allowed.

Necessary conditions: P1. an aborted writer must not hold the reader back once it resolves; P2. a poll after release must make the position move or deliver; P3. a drain terminates.

Limit of the black-box view: a reader that loses an event (an omission) and then answers empty looks, at quiescence, like a stalled one, so such a mutant fails both Safety and Progress. A reader that is correct but slow fails only Progress or Eligibility (mutants "stalls before every batch" and "waits one tick past T").

A scenario cannot claim what it does not test: Progress with no quiescent drain, or a claimed bound with no event ever due, fails with `not-exercised`.

## 3. The oracle

`Truth` is the ground-truth log. The script feeds it (begin, append, commit, abort, tick) at the same time as it drives the backend. `Evaluate(trace)` derives every expectation from `Truth` and the recorded deliveries: the entitled set per consumer (`Truth.Confirmed`, sorted by the ordering key for messages only), what had committed at each poll, what was due by `commit + T`. It never reads, decodes or compares a cursor. A test feeds it hand-written traces with no reader at all, and another gives it a reader whose cursor is garbage but whose deliveries are right, which must pass.

## 4. Scenario catalogue

All are plain data in `catalogue.go` (`Catalogue()`), executed by `harness_test.go` against two correct fakes, by `mutants_test.go` against mutants and by `testkit_test.go` against testkit. `Generate(seed)` adds seeded interleavings (executed for seeds 1 to 40).

| Scenario | What it provokes | Properties |
|---|---|---|
| `late-commit-issue-case` | a@100 and b@200 commit, late@150 (appended earlier by a concurrent transaction) commits after the reader passed 200 (the case in #348) | S, E, P |
| `late-commit-lower-id-higher-timestamp` | lowest insertion id with highest timestamp commits last | S, P |
| `long-transaction-bounded` | a writer open for LMax ticks while two others commit; bound T after each commit | S, E, P |
| `aborted-transaction-releases-the-reader` | rollback must release the reader; the aborted event never appears | S, P |
| `batch-limit-inside-a-tie-group` | five equal ordering keys, limit 2, three single polls then a drain | S, P |
| `tie-group-completed-by-a-later-commit` | an event with an already delivered ordering key commits afterwards | S, P |
| `retry-redelivers-with-a-stable-signal` | two batches read without persisting the cursor (duplicates) | S, P |
| `restart-resumes-from-the-persisted-cursor` | two restarts from a persisted cursor while events keep committing | S, P |
| `restart-before-the-cursor-was-persisted` | crash after delivery, before the cursor commit | S, P |
| `selection-by-scope` | `OneScope(Unscoped())`, one tenant and `AllScopesInCell` side by side, with a late commit | S, P |
| `selection-rejection` | wildcard without privilege and zero `Scope` refused | S |
| `selection-by-slice-range` | range 2..3 and all slices, with a late commit | S, P |
| `eligibility-conditions-broken` | a transaction open 50 ticks with LMax 5 and broken conditions | S, P (E not claimed) |

Not in the catalogue: concurrent writers on the same persistence id (a write conflict is the store's job, `RunEventsStoreConformance`), cursor-header rejection (#351), and failure or saturation of a real database (integration lane).

## 5. Proof that the harness detects defective readers

`fakes_test.go` holds two correct readers with different mechanisms (stable prefix of the insertion order; set of delivered positions) and the mutants. `mutants_test.go` asserts the exact property and rule that fails for each; `harness_test.go` asserts that both correct readers pass every scenario.

| Mutant | Fails |
|---|---|
| Skips a late commit (timestamp cursor, as today's `GetShardEvents`) | Safety `omission` on `late-commit-issue-case` |
| Skips a late commit (insertion-id cursor) | Safety `omission` on `late-commit-lower-id-higher-timestamp` |
| Skips a tie group completed by a later commit | Safety `omission` |
| Cuts a tie group at the batch limit | Safety `omission` on `batch-limit-inside-a-tie-group` |
| Duplicates with a fresh token on every delivery | Safety `dup-token` on `retry-...` |
| Ignores the scope selection | Safety `wrong-selection` on `selection-by-scope` |
| Ignores scope validation | Safety `not-rejected` on `selection-rejection` |
| Ignores the slice range | Safety `wrong-selection` on `selection-by-slice-range` |
| Resumes one event past the cursor after a restart | Safety `omission` on `restart-resumes-...` |
| Delivers events that never committed | Safety `phantom` |
| Stalls three polls before every batch | Progress `stall`, Safety still passes |
| Makes committed events wait one tick past T | Eligibility `late-visibility`, Safety still passes |

Against testkit's `EventsStore` (`testkit_test.go`): a store-backed reference reader passes all scenarios; a shim over the **current** `GetShardEvents(scope, shard, offset, limit)` fails the five scenarios in which an event commits behind the cursor and passes the rest. The known failures are declared per scenario and per property, with their exact status and codes, in `legacyKnownFailures` (`testkit_test.go`): Safety `omission` and Progress `stall` in all five, and Eligibility `late-visibility` only in `late-commit-issue-case` and `long-transaction-bounded`. `Deviations` rejects any other code and any other failing property, and a scenario absent from the map must pass entirely. testkit is an in-memory model, not the PostgreSQL adapter. The two subjects (`LegacyReader`, `StoreSetReader`) are the exported ones of `reference.go`, so testkit and PostgreSQL evaluate the same code.

Against a real PostgreSQL (`inttest/flows/reader`, integration lane): one catalogue scenario, `late-commit-issue-case`, with writers that are real transactions on their own connections. The current `GetShardEvents` fails with the exact baseline measured by running this case (the known failure, asserted through `Deviations`): Safety `omission`, Eligibility `late-visibility` and Progress `stall`, and nothing else. The store-backed reference reader over the same adapter and the same writers passes the whole oracle, so the omission belongs to the timestamp cursor and not to the backend. The other four legacy known failures are not repeated there: they are the same timestamp-cursor behavior and are covered by the unit run.

## 6. Acceptance criteria matrix (live body of #348, read 2026-10-07)

| Criterion | Document | Executed test | Pending |
|---|---|---|---|
| The omission check fails deterministically with the current PostgreSQL adapter and is marked as a known failure | Sections 2.1, 4 (`late-commit-issue-case`) | Same scenario fails, deterministically, on the legacy timestamp cursor over testkit and on the timestamp-cursor mutant; the known failures are declared with exact statuses and codes in `legacyKnownFailures`. Over the real adapter: `TestLateCommitOverPostgreSQL` in `inttest/flows/reader` asserts the omission and the exact per-property baseline with real transactions (deterministic over repeated runs; it fails if the legacy reader is replaced by a correct one or if an additional code or failing property appears) | The run exists and passes locally with Docker. Its execution in the CI integration lane (push to `develop`, release PR) is pending; D5 is chosen provisionally as an assertion, not a skip |
| Eligibility and progress checks do not promise application latency | Section 2.2, 2.3 | By construction: the harness has no apply step, the handler never fails, and only delivery to the consumer is measured. No test can assert an absence | none |
| All run against testkit too | Section 5 | `TestScenariosRunAgainstTestkit`: every scenario on testkit's `EventsStore`. The reader is a harness reference reader, because testkit has no reader | **Pending approved SPI**: a testkit-native reader running the suite through the final interface |
| Objective: safety case a@100, b@200, late@150, two concurrent transactions | 4 | `late-commit-issue-case` | `TestLateCommitOverPostgreSQL` (two real transactions, the late one committing after the reader passed 200) |
| Objective: eligibility, event readable within T with a bounded long transaction | 2.2, 4 | `long-transaction-bounded` (correct readers pass, delaying mutant fails) | Measuring T on a real database under real timeouts |
| Objective: progress, cursor advances when the transaction is released | 2.3, 4 | `aborted-transaction-releases-the-reader`, issue case, stall mutant | none |
| Objective: handler that never fails | 1 | the runner persists after every batch | Runner errors are I-22 |

Cross-checks against #351 (so its author can reuse the oracle): `OneScope(Unscoped())` valid and distinct, `AllScopesInCell` privileged: **executed**. Cursor rejection cases, selection fingerprint, header, portability rules, offset and marker identity, deprecation plan: **specified in #351, not covered here, pending approved SPI**.

## 7. Decisions that need maintainer approval

| # | Decision | Chosen here (provisional) | Alternatives |
|---|---|---|---|
| D1 | Where the harness lives and what is exported | **Approved by the maintainer** (recorded on PR #445): a shared harness in `persistence/conformance/readertck`, outside `internal`, with a minimal API for tests and no dependency from production code. It left `internal` because `inttest` is a separate module and Go forbids importing another module's internal package; the alternative to a second copy of the scenarios is this export. The API itself stays without a compatibility promise until the reader SPI is approved | Rejected: move it back under `internal` and keep the PostgreSQL run in a package rooted there; export a minimal `Run...ReaderConformance` in `persistence/conformance` once the SPI is approved; keep it in a `testkit` sub-package |
| D2 | Duplicate policy | Any redelivery is allowed if the token is stable and unique | Only inside the unpersisted window; forbid duplicates (needs exactly-once, not offered) |
| D3 | Meaning of `limit` | Not constrained (a poll with `limit` >= 1 must be able to progress) | Hard cap on batch size; keep today's minimum-batch target |
| D4 | Progress numbers | `EmptyPollBudget` 2, `Settle` 0, final catch-up budget generous | Stricter or looser budgets per mechanism, once #352 has data |
| D5 | How the PostgreSQL known failure is marked | An assertion that the check fails (it flips red when the reader is fixed), in `inttest/flows/reader`. Provisional, awaiting confirmation | A skip with a reason: not possible, because the `inttest` lane forbids skips |
| D6 | Is a bounded-eligibility claim mandatory or an optional adapter capability | Optional, only judged under declared conditions | Mandatory capability with the conditions as a startup check (links to ADR #347 D5) |
| D7 | Ordering guarantee | Per persistence id only | Also a causal order across aggregates of the same transaction |
| D8 | Scope enumeration | The harness gives the reader the cell's scopes; `EventsStore` cannot list scopes | A capability in the SPI for `AllScopesInCell`; decided in #351 |

The ADR on module topology (#347) is still Proposed and its merge is not approval of any of the above. No `go.mod`, schema, GoAkt or production code is changed.

## 8. Reproduce

```sh
go test -race -count=1 ./persistence/conformance/...
go run ./.github/scripts/unitgate -strict
```

The PostgreSQL run needs Docker and belongs to the integration lane only, never to a unit or feature build:

```sh
go -C inttest test -count=1 -race ./flows/reader/
```

With Colima, point Testcontainers at its socket: `DOCKER_HOST=unix://$HOME/.colima/default/docker.sock TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock`.
