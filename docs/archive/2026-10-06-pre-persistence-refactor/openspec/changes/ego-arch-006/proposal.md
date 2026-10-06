# Proposal — Physical Go modules and impact-aware CI (EGO-ARCH-006)

| Field | Value |
|---|---|
| Change | `ego-arch-006` |
| Date | 2026-09-27 |
| Phase | `sdd-propose`: proposed (architecture decision record; ADR) |
| Tracker | [`#102`](https://github.com/getsyntegrity/ego/issues/102), parent [`#10`](https://github.com/getsyntegrity/ego/issues/10), CI owner [`#38`](https://github.com/getsyntegrity/ego/issues/38) |
| Baseline | `main` at `77beda6b91646b0e031ce78401ee997fd919bd65` |
| Evidence | [`exploration.md`](./exploration.md) (graphs, proxy resolution, selector behavior, CI runs); [`design.md`](./design.md) (module map, decisions, selector, slices) |
| Builds on | ego-arch-001 (`openspec/changes/ego-arch-001/design.md` §3 rules, §6 module criterion, §8 versioning) |
| Human gates | D1 module identity, D2 nested-module path/layout/versioning, D3 first published version and release order, D7 contracts-module contents, D8 how §6(1)/§6(4) are met before any release (design §3) |
| Related | [`#39`](https://github.com/getsyntegrity/ego/issues/39) release identity, [`#122`](https://github.com/getsyntegrity/ego/issues/122) / [#130](https://github.com/getsyntegrity/ego/pull/130) publisher test closures, [`#123`](https://github.com/getsyntegrity/ego/issues/123) / [#128](https://github.com/getsyntegrity/ego/pull/128) `port/behavior`, [`#105`](https://github.com/getsyntegrity/ego/issues/105) / [#125](https://github.com/getsyntegrity/ego/pull/125) composition root, [`#124`](https://github.com/getsyntegrity/ego/issues/124) final layout, [`#112`](https://github.com/getsyntegrity/ego/issues/112) root test latency |

## Why now

#102 wants Ego's architectural boundaries to become real Go modules, and CI to run only what a change can affect. The package work of ego-arch-001 is done for the publisher contract (S1a, S1b), and #111 made CI verify every nested module. The next step needs three answers first: which boundaries become modules, how CI decides what a change affects when modules depend on each other, and whether the modules can be consumed once they exist. The exploration measured all three on `main`:

1. **Package extraction has gone as far as it can for the publishers.** Their production build compiles no GoAkt. But each publisher still *requires* the root module, so its module graph keeps 173 GoAkt edges and 35 Olric edges (exploration §4.2). Only a module below the root removes them. That is the benefit ego-arch-001 §6 criterion 2 asks for.
2. **The selector cannot follow a module that depends on another module.** It models only nested-module-to-root imports. It treats `go.work` as a root package file instead of a global change. And it misses a new `go.mod` carved out of a root directory (exploration §6). Today every root Go change outside the tooling under `internal/cmd` and `migration` selects all six nested modules.
3. **Nested modules in this repository cannot be consumed, at least at the baseline commit.** A path like `github.com/pablogore/ego/v4/publisher/kafka` in directory `publisher/kafka` does not resolve from the module proxy, or even directly from the Git repository (exploration §5). Any new module built the same way would inherit that, and a contracts module the root requires would make the root itself unconsumable.

## Intent

Adopt a module topology and a selection rule that satisfy #102 without breaking ego-arch-001 §6:

- **Selector first.** `ciselect` reads every `go.mod`, builds the graph of in-repository requirements, and selects every module that transitively depends on a changed one. It gives a reason chain for each module, forces the full gate on global paths, and treats a new or removed `go.mod` as a boundary change. No list is maintained by hand.
- **Then a few modules, one per pull request, each passing §6:**
  1. an unreleased `test/compat` integration module;
  2. one contracts module holding `egopb`, `port/publishing` and `port/adapter` (plus `port/adapter/adaptertest` once its own tests stop importing root packages): the root packages the publishers import, production and tests (D7 option i, amended 2026-09-28 in design §3 "D7 amendment");
  3. the four publishers switched to require only the contracts module.

  `port/behavior` (PR #131) stays in the root under that recommendation. It imports `command`, which imports `tenancy`, and both stay in the root. Putting `port/behavior` in the contracts module alone would create a module cycle, which ego-arch-001 §3 forbids, and would pull GoAkt back into the publishers' module graph. D7 (ii), moving `command` and `tenancy` along, is the alternative.
- **Everything else stays in the root module until #124.** That includes the GoAkt runtime adapter, `port/behavior`, the root-level contracts and `testkit`. The major release #124 already plans is the natural point for a second import-path change.

This change is documentation only.

## In scope (decisions this proposal closes)

- **Target module map and order** (design §2), with each boundary checked against ego-arch-001 §6, plus the boundaries rejected and why.
- **Selector design** (design §5):
  - module edges from `go.mod` requirements through `go mod edit -json`;
  - the reverse-transitive closure;
  - root-lane seeding through a dependency;
  - global paths, the boundary-change rule, and `plan.json` as the portable plan for #38 and Shipwright;
  - fixtures for the five cases #102 names.
- **Verification rules around `go.work`**: CI never verifies in workspace mode; integrated verification uses `replace`; published verification uses neither (design §4).
- **Wave 3 slices S0–S3**, each with file ownership, checks and a before/after CI measurement plan (design §6), and follow-ups F1–F5.

## Out of scope (MUST NOT in this change)

- Creating, moving or deleting any Go module, package, `go.mod`, workflow or script. S0–S3 do that later, each in its own PR.
- Choosing the module identity, the nested-module path scheme, the first version, the contracts-module contents, or whether to amend ego-arch-001 §6 and §8. Those are D1–D3, D7 and D8, left to the maintainers.
- Publishing or tagging anything, and changing `release.yml`. That belongs to #39 and to follow-up F4.
- Root test latency: #112. Design §1 explains why modules do not shorten contract-change feedback.

## Approach and rejected alternatives

Recommended: **selector first, then a few §6-qualified modules, gated on the path decisions** (exploration §10, option 3). Rejected:

- **One module forever, with better selection only.** It cannot prune the publishers' requirement lists, and it leaves #102's "boundaries are real modules" unmet.
- **Many modules now, at today's `…/v4/<dir>` paths.** Every one of them would be unresolvable (exploration §5), and multiplying modules before the selector follows nested-to-nested edges would leave changes unverified.
- **A separate schema module for `egopb` under a port module.** The schema module alone fails §6(2): its only effect is letting the port module avoid requiring the root, and one contracts module does that with one release unit fewer.
- **A tools or `migration` module as the first "leaf".** Both fail §6(2): they remove nothing from anyone's requirement list. The first leaf is `test/compat`, which the #122 lane needs anyway, and which exercises the first nested-to-nested edge.

## Decisions (approved by the maintainers on 2026-09-27)

Design §3 gives the options and tradeoffs. The maintainers approved D2–D7 as recommended and D8 option (C). They approved D1's direction, "migrate the module path before the first release", with one condition: execution waits for explicit confirmation of the concrete target paths and migration plan in design §3 ("D1 target path and migration plan"). These decisions guide S2 and S3. The recommendations as recorded:

| # | Decision | Recommendation | Blocks |
|---|---|---|---|
| D1 | Module identity: keep `github.com/pablogore/ego/v4` or move to `getsyntegrity` | Decide before S2. If a move is planned, do it before the first release (zero tags exist today, though the root already resolves by pseudo-version). | S2, S3 |
| D2 | Nested-module path, layout and versioning | (a) Paths without the root's major suffix (`…/ego/contracts`, `…/ego/publisher/kafka`), with independent **v0/v1** versions tagged `<dir>/v0.x.y` or `<dir>/v1.x.y`. Go allows v2+ only on a path ending in `/vN`, so the alternative (a') is lockstep `…/contracts/v4`, tagged `contracts/v4.x.y`. Either way the new module needs a **new directory**, and the old directories keep root alias packages. | S2, S3 |
| D3 | First published version and release order | Follows D1 and #39. Releases become topological: contracts, then root, then publishers. That explicitly **amends ego-arch-001 §8, policy item 1** ("root released first"). | S2 consumability, release |
| D4 | `go.work` policy | Generated on demand by a script, not committed. CI always runs with `GOWORK=off`. | F5 only |
| D5 | Allow new *unreleased* integration modules (like `benchmark`) | Yes, provided no released module requires them. | S1 |
| D6 | Confirm that CI speed alone never justifies a module (ego-arch-001 §6(2)) | Confirm. | — |
| D7 | Contents of the contracts module(s) | (i) `egopb` + `port/publishing` only; `port/behavior` waits for F1. **Amended 2026-09-28:** also `port/adapter`, and `adaptertest` under a no-cycle precondition (design §3 "D7 amendment"). Alternatives: (ii) also `port/behavior`, `command` and `tenancy`; (iii) two modules, schema plus port (rejected). | S2, S3 |
| D8 | §6(1) needs "one release" and §6(4) needs a published-verification job, and neither exists | (C): gate S2 on the release pipeline (F4), and amend §6(1) to count `main` builds since #117. Alternatives: (A) gate on F4 and a first release; (B) amend both. | S2, S3 |

S0 (the selector) and S1 (the first leaf module and nested-to-nested edge) proceed now. S2 and S3 stay gated on F4 (the release pipeline with published-version verification, per D8 (C)) and on maintainer confirmation of the D1 target path. No module that the root requires is created before then.

## Affected public consumer surfaces

None in this change. When the slices land:

- **S2** gives `egopb`, `port/publishing` and `port/adapter` (plus `port/adapter/adaptertest` once its tests stop importing root packages; D7 amendment) new import paths under D2 (a) or (a'). Once a directory holds its own `go.mod`, the root can no longer serve the old path from it, so the new module lives in a new directory. The old directories stay in the root as **alias packages** until #124, per the window recorded in #128. The aliases are needed even though no release exists, because the root already resolves by pseudo-version (exploration §5) and someone may be pinned to a commit.
- **S3** changes the publishers' `go.mod` requirements and imports and, under D2, their module paths. At the baseline commit they cannot be resolved, so no consumer can depend on their old path. #130's `closure_test.go` hard-codes the root path and is updated in S3.
- **S0 and S1** change only CI tooling and test code.

## Rollback

Revert `openspec/changes/ego-arch-006/`. For the later slices:

- **S0** reverts cleanly, because `modules.json` keeps its shape.
- **S1** reverts by restoring the four `compat_test.go` files.
- **S2–S3** revert cleanly only before a release exposes the new module paths. After that, the modules stay, and only in-repository callers can be reverted.

## Risks

Design §7 has the full list. The main ones:

- **Release and tag scheme.** The current publisher tag scheme cannot be resolved (exploration §5).
- **Version skew.** Integrated verification with `replace` can pass while consumers fail. Mitigation: published verification for every released module (F4).
- **Cross-module `internal/` imports.** archcheck covers only nested-to-root today; S1 generalizes the rule.
- **Import-path churn with #124.** Mitigation: keep alias packages at the old root paths until #124, even before the first release, because pseudo-version consumers exist.

## Success criteria (acceptance of this ADR, mapped to #102)

- [ ] A normative module map with ownership and allowed dependencies exists, with each module justified against ego-arch-001 §6 (design §2).
- [ ] The current module inventory, import graph, module graph and CI baseline are recorded with run IDs and reproducible commands (exploration §3–§8).
- [ ] The selector design derives selection from the real `go.mod` graph with no manual list, forces the full gate on global paths, detects new modules, and defines fixtures for the leaf, shared-contract, transitive-consumer, new-module and global-change cases (design §5).
- [ ] The `go.work` policy and the verification rules are stated, and every part that depends on an open decision is named (design §3, §4).
- [ ] Wave 3 is sliced one module per PR, selector first, each slice at most five tasks with checks and a CI measurement plan; overflow is named as follow-ups (design §6).
- [ ] No production code, CI file or `go.mod` changes in this change.
