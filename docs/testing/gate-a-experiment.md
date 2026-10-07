# Gate A experiment protocol: reader mechanism comparison (#352, #387)

Status: PROPOSAL for the experiment, not results. No mechanism is chosen and nothing here is a measurement. Gate A has no recorded result: the criteria of #387 are unchecked, `benchmark/` holds no mechanism result, and the workload model of #355 has no figures. This document fixes HOW the experiment will run so that every candidate is judged by the same oracle, the same control, the same load and the same failure criteria, and states what blocks it.

Sources, each fact below is tagged by where it comes from: **#352**, **#387**, **#355**, **#348** (the oracle, PRs #442 and #445), or **Proposed** (this document, needs approval).

## 1. What the live issues require

| Source | Requirement |
| --- | --- |
| #352 | Decide the adapter mechanism in a cell (xid8, per-slice counter, batched publication), including a tenant that needs strict latency isolation. Criteria: 0 omissions and progress per I-02; throughput >= 80 % of the control on the hot shard; p99 <= `transaction_timeout` + 1 s with a long transaction in another database; results in `benchmark/`. Depends on #348 and #355. xid8 is a candidate, not a decision. If it fails, compare the counter with 64 slices and batched publication. Eligibility is distinguished from the application SLO; the operating conditions of the bound <= T and the hardware are recorded. |
| #387 (Gate A) | Zero omissions, eligibility conditioned <= T, progress, throughput >= 80 % of the control, experimental p99 and overload results WITH AN APPROVED MODEL. Configuration, hardware, limits and failure criteria published. If it fails, compare alternatives. |
| #355 | Defines the counting unit, load distribution, projection fan-out, saturation scenarios and measurable objectives that "the Gate A experiment will use": commands/s, events/s, bytes, handler cost and fan-out, hot tenants and entities; SLOs that separate availability, in-quota rejections, write confirmation, eligibility and application; limit budgets; initial values labeled as hypotheses; no promise of 1M r/s without measurement. |
| #348 / #442 / #445 | The oracle: Safety, Eligibility, Progress judged separately; scenarios as data; the omission case runs on a real PostgreSQL. |
| #358 | The schema is defined AFTER the mechanism is chosen, with #351 and #352 and #387 as its dependencies. |

Consequence: **#358 must not start, and no product schema may change, until Gate A records a result.** The experiment uses throwaway databases in `benchmark/`; its candidate schemas are experiment fixtures, not product schema.

## 2. Candidates and the control

| Id | Candidate | Source |
| --- | --- | --- |
| C0 | Control: the current reader, `GetShardEvents` with a timestamp cursor, per `(scope, shard)` | the current code; the baseline for the 80 % criterion |
| C1 | Transaction-id horizon (xid8) with a tie-breaker for several events of one transaction | #352 |
| C2 | Counter per slice. Variant with 64 counters when C1 fails | #352 |
| C3 | Batched publication | #352 |

Each candidate is adapted to the same two harness interfaces (`Backend`, `Subject`) so it is judged by the same oracle. The adaptation of a candidate is part of the experiment code and is reviewed as such; it does not enter the product.

The control is defined once and not tuned: same hardware, same database configuration, same load, same hot shard, and the legacy reader's own batch size. If C0 is not reproducible between two runs of the same load, the 80 % criterion cannot be evaluated (see failure criteria).

## 3. One oracle

- Correctness (Safety, Eligibility, Progress) is judged by the `readertck` oracle of #348 (`Evaluate`), never by a per-candidate check.
- The deterministic scenarios of the catalogue run first against every candidate over a real PostgreSQL (the pattern of `inttest/flows/reader`). A candidate that fails Safety on the catalogue is out before any load is spent.
- Under load the experiment records its own trace in the format that `Evaluate` consumes, so the same rules judge a load run (zero omissions over millions of events, not only over a script). **Open**: the recorder that turns a live run into a `Trace` does not exist yet and must be built with the experiment; if the existing `Truth` cannot take a live feed without a change, that change is part of this work, in `readertck`, and is reviewed separately.
- Application latency is never measured here. Eligibility (commit to readable) is measured and reported apart from the application SLO of #355.

## 4. Workload: what is fixed here and what #355 must provide

The experiment does not invent a workload. It needs the following from the approved model of #355. Where #355 gives only a hypothesis, the run is labeled exploratory and cannot satisfy #387.

| Input | Needed for | What #355 says today | Gap |
| --- | --- | --- | --- |
| Counting unit (a command, an event, a request) | every rate | It asks to define it | no value |
| Commands/s and events/s per cell, and events per command | write load | It asks for a document with these figures | no figures |
| Payload size distribution | bytes, row size | asks for bytes | no distribution |
| Hot tenant and hot entity skew | the hot-shard criterion (C0 vs candidate) | asks for hot tenants and entities | no skew; and the slice count is now fixed at 1024, so "hot shard" must be restated as a hot slice and its share of the load |
| Projection count and handler cost per event | read load, backlog | asks for fan-out and handler cost | no values |
| Concurrent writers, long-transaction rate and duration | the horizon behavior of C1 | not covered | the long-transaction injection is not specified in #355 |
| Saturation scenarios (burst, sustained, recovery) | overload results required by #387 | "saturation scenarios" | no definition; the PRD's burst and sustained figures are provisional experiment inputs, not commitments |
| Observation windows and warm-up | stable percentiles | asks for observation windows | no values |
| Hardware class | reproducibility | not covered | must be recorded by the run, and the hardware class chosen before the first run |

**Proposed** request to #355: add the long-transaction injection (rate, duration, which database), the hot-slice share, the hardware class and the warm-up and window lengths. Until #355 is approved with these, only exploratory runs are possible.

## 5. Procedure (Proposed)

1. Fix and record the configuration before any run: PostgreSQL version and image, hardware, kernel, CPU and memory limits, `transaction_timeout` for every writing role, `max_prepared_transactions`, dedicated cluster or not, autovacuum settings, and the number of slices (1024).
2. Run the catalogue of `readertck` against each candidate (correctness gate).
3. Warm up, then run each load for the observation window, C0 and each candidate alternately in the same session to cancel drift. At least the number of repetitions needed for a confidence interval is decided before the first run, and every run is kept.
4. Inject the long transaction in ANOTHER database of the same cluster for the p99 criterion of #352, with the transaction open for the duration stated in the model.
5. Run the overload scenarios of the model and the recovery after them.
6. Judge by section 6 and write the result in the format of section 7. A failing candidate is recorded as failing, with the data, not omitted.

## 6. Failure criteria (fixed before the run)

A candidate FAILS Gate A if any of these holds. They are stated here so nobody sets them after seeing data.

| Criterion | Source | Fails when |
| --- | --- | --- |
| Zero omissions | #352, #387, #348 | the oracle reports any `omission` in the catalogue or in any load run |
| Progress | #352, #348 | the oracle reports `stall` or `no-convergence` once the blocking transaction is released |
| Throughput | #352, #387 | throughput on the hot slice is below 80 % of C0 measured in the same session |
| p99 with a long transaction | #352 | p99 of eligibility exceeds `transaction_timeout` + 1 s with the long transaction in another database |
| Conditional eligibility | #387 | the bound <= T is claimed while one of its operating conditions (dedicated cluster, timeout for every writing role, no prepared transactions, alert on the oldest transaction id) was not met in the run |
| Reproducibility | this document | C0 differs between two runs of the same load by more than the stated tolerance, or the configuration was not recorded |
| Overload | #387 | the model's overload scenario loses events, or recovery does not complete in the time the model states |

Safety failures are not traded against speed: a candidate that omits is rejected whatever its throughput.

## 7. Results (Proposed format and place)

`benchmark/results/gate-a/` in the `benchmark` module, one file per run, plus a summary:

- the full configuration of step 1, the load definition and its source version in #355;
- per candidate: oracle verdict per property, throughput, eligibility percentiles (p50, p99, max), resource use, and the failure criteria it met or failed;
- the raw data or a pointer to it, so the summary can be reproduced.

Gate A is recorded in #387 only when: every candidate has a result against the same model; the chosen mechanism passes section 6, or the alternatives have been compared as #352 requires; and the maintainers record the decision. Only then can #358 start.

## 8. What blocks the experiment today

| Blocker | Owner |
| --- | --- |
| The workload model of #355 has no approved figures (items in section 4) | #355 |
| The live-run trace recorder for the oracle | experiment code (this work, once the model exists) |
| Candidate adapters C1 to C3 on the harness interfaces | experiment code |
| Hardware class and repetition count to be chosen | maintainers, with #355 |
| The omission case of #348 over PostgreSQL is in PRs #442 and #445, not merged | #348 |

## 9. What this document does not do

It chooses no mechanism, records no result, changes no schema, `go.mod` or production code, and does not enable #358.
