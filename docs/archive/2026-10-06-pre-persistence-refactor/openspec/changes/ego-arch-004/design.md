# Design — Adapter SPI, capabilities and lifecycle (EGO-ARCH-004)

| Field | Value |
|---|---|
| Change | `ego-arch-004` |
| Date | 2026-09-27 |
| Phase | `sdd-design` |
| Tracker | [`#106`](https://github.com/getsyntegrity/ego/issues/106), parent [`#10`](https://github.com/getsyntegrity/ego/issues/10) |
| Inputs | [`exploration.md`](./exploration.md), [`proposal.md`](./proposal.md); ego-arch-001 [`design.md`](../ego-arch-001/design.md) §2, §3, §10; ego-arch-003 [`design.md`](../ego-arch-003/design.md) §D2, §D5–§D8, §9; ego-arch-006 [`design.md`](../ego-arch-006/design.md) §3 D1, D7, D8, §6; issues [`#146`](https://github.com/getsyntegrity/ego/issues/146), [`#147`](https://github.com/getsyntegrity/ego/issues/147), [`#148`](https://github.com/getsyntegrity/ego/issues/148) |
| Baseline | `main` at `f2b5130148086d95a4d6362971b85e800b0ce155` |

## 1. Summary and vocabulary

Ego already has contracts for every adapter family (stores, publishers, encryptor, tenant resolver) and, since #105, one composition root that wires them. What it lacks is a common layer on top of those contracts: a way for any adapter to say what it is and what optional things it can do, a written contract for how it starts and stops, a shared test suite that proves both, and a rule that keeps adapters from depending on the composition root. This design adds that layer without changing any existing interface, so it is additive in v4.

In one paragraph: a new contract package, `port/adapter`, defines a small `Descriptor` (which port an adapter serves, and a name for its implementation) plus a list of declared capabilities. A **capability** is an optional behavior with a method behind it; the method lives in an optional interface in the contract package that owns the port, exactly as `tenancy.FixedTenantResolver` does today. An adapter declares the capability in its descriptor *and* implements the interface; the composition root checks, before anything starts, that the two agree; the conformance suite checks the same thing in the adapter's own tests. Core code asks one accessor per capability instead of asserting interfaces where it happens to need them. Lifecycle follows the ownership rules ego-arch-003 §D5 already fixed; this design adds what each adapter must guarantee (`Start` cleans up after itself, `Close` is idempotent and bounded, `Ping` is a readiness probe) so that #105's rollback actually releases everything.

Terms used below:

- **SPI (service provider interface)** — the contracts a third party implements so Ego can use its code.
- **Port** — one contract an adapter implements, such as `persistence.EventsStore` or `publishing.EventPublisher`.
- **Slot** — one field of `compose.Spec` that holds an adapter, such as `Spec.EventsStore` or one element of `Spec.EventPublishers`.
- **Borrowed adapter** — an adapter the consumer keeps owning (stores, encryptor, tenant resolver, telemetry, logger, per ego-arch-003 §D5). The composition root may probe it, never open or close it.
- **Owned adapter** — an adapter whose ownership moves to the composition root when `New` succeeds (publishers, per §D5). The composition root starts it, if it can be started, and closes it on every terminal path.
- **Capability** — an optional behavior of one port, identified by a stable string and backed by an optional Go interface.
- **Conformance suite** — a reusable test package an adapter author runs from their own tests to prove the adapter honors a contract.

## 2. Scope boundary with #105, #11, #24 and #37

| Concern | Owner | This design |
|---|---|---|
| Slots, static validation V1–V7, `StartError`, start/stop order, cleanup context | #105 (`compose`, `compose/goakt`, `compose/internal/lifecycle`) | Uses them; adds one rule, V8 (§D6), and one change inside step 4 (§D4) |
| Which adapter owns what | #105 §D5 | Restates it per adapter; changes nothing |
| Runtime SPI, runtime capability negotiation (RUNTIME-006) | #11 | Offers `port/adapter` as the shared vocabulary; does not design the runtime port |
| Drain and flush during shutdown (LIFE-003, LIFE-004), timeouts (LIFE-007), restart (LIFE-008), transferring store ownership (LIFE-006) | #24 | Reserves nothing with semantics; records every interaction in §7 |
| Adapter/SPI versioning and compatibility ranges (COMPAT-005) | #37 | Keeps `Descriptor` extensible so #37 can add a field additively |
| Observability contract (telemetry is still `*ego.Telemetry`, `telemetry.go:32-37`) | #31 | Out of scope; telemetry stays a borrowed value, not a port |

**How this vocabulary fits #147 and #148.** [#147](https://github.com/getsyntegrity/ego/issues/147) (RUNTIME-001/002, maintainer decisions recorded in the issue on 2026-09-27) defines `port/runtime` as small interfaces, one per capability, plus a composite interface. [#148](https://github.com/getsyntegrity/ego/issues/148) (the in-memory runtime and `compose/inmem`) requires that an operation a runtime does not support return an explicit typed error, not panic. Those two rules are the *consumer* side of a capability: which interfaces a runtime value implements, and what a caller gets when it calls something unsupported. This design is the *provider* side: what an adapter declares, so the composition root can check it before anything starts. They compose rather than compete. A runtime adapter can carry a `Descriptor` whose capabilities name what it supports. The runtime port is the exception to V8b's two-way check (§D6). The S4 runtime design, merged as [`openspec/changes/ego-runtime-001/design.md`](../ego-runtime-001/design.md) §8, gives `port/runtime` a composite interface, so every runtime implements all four capability interfaces. For example, the in-memory runtime of #148 answers projection calls with `runtime.ErrUnsupported`. The method set therefore says nothing about support. For the runtime port, V8b must not apply the implemented ⇒ declared direction, and runtime capabilities are declaration-only (RUNTIME-006). #148's typed "unsupported" error stays the call-time answer for anything that cannot be known statically. #147 itself says the provider side of the runtime follows #106's model; this design does not define any runtime interface. See follow-up F-E.

## 3. Decisions

These are the decisions this design proposes; they become accepted when the pull request that carries them is approved. The choices that needed the maintainers (O1–O7, and the chain of specs) were made on 2026-09-27 and are recorded in §9.

### D1 — Where the SPI lives

A new contract package `port/adapter` (`github.com/pablogore/ego/v4/port/adapter` until the ego-arch-006 D1 path migration). It follows ego-arch-001 §2: a new top-level contract is born under `port/`. It imports only the standard library, so `contract-allowlist` applies to it unchanged, and it gets a `go list -deps` architecture test like `port/publishing/publishing_architecture_test.go`, with an empty allowlist.

It holds only what is common to every port. Capabilities specific to a port are declared in that port's own package (§D3), so `port/adapter` never imports another contract and never grows a list of every capability in the framework.

Sketch (names are settled in slice SPI-1; the semantics are not):

```go
package adapter

// Port names the contract an adapter implements, e.g. "persistence.EventsStore".
type Port string

// Capability names one optional behavior of a port, e.g. "tenancy.fixed-tenant".
type Capability string

// Descriptor is what an adapter says about itself. It is a value, so it can be
// logged, compared and extended with new fields without breaking anyone.
type Descriptor struct {
	Ports        []Port       // every contract this value implements; one type often serves several
	Name         string       // implementation name, e.g. "kafka", "testkit-memory"
	Capabilities []Capability // optional behaviors it declares; order irrelevant
}

// Describer is implemented by adapters that declare a Descriptor. Optional.
type Describer interface{ Describe() Descriptor }

// Starter is implemented by an owned adapter that has work to do between
// construction and first use (dial a broker, open a producer). Optional.
type Starter interface{ Start(ctx context.Context) error }

// Pinger is the readiness probe. Every store contract already has it.
type Pinger interface{ Ping(ctx context.Context) error }

// Describe returns v's Descriptor and true, or the zero Descriptor and false
// when v does not implement Describer.
func Describe(v any) (Descriptor, bool)

// StarterOf and PingerOf return v as a Starter / Pinger and true, or nil and
// false when v does not implement it.
func StarterOf(v any) (Starter, bool)
func PingerOf(v any) (Pinger, bool)

// Declares reports whether d lists c; Serves reports whether d lists p.
func (d Descriptor) Declares(c Capability) bool
func (d Descriptor) Serves(p Port) bool
```

`Starter` and `Pinger` are lifecycle capabilities. They get constants `adapter.CapStart` and `adapter.CapReady` so a descriptor can declare them like any other capability. **`port/adapter` owns the only type assertions on `Describer`, `Starter` and `Pinger`** (in `Describe`, `StarterOf` and `PingerOf`). V8b, `compose/goakt` step 4 and `adaptertest` all go through these three functions and never assert the interfaces themselves.

`Ports` is a list because one Go type often implements several contracts. For example, a Postgres store type may be both an `EventsStore` and a `SnapshotStore`, and the same value can then sit in two slots.

### D2 — Identity

An adapter's identity has two parts, kept separate on purpose because today they are conflated (exploration §2):

- **Adapter type:** `Descriptor.Ports` plus `Descriptor.Name`, for example `{Ports: ["publishing.EventPublisher"], Name: "kafka"}`. Error messages, logs and capability validation use it. The slot's port must be one of `Ports` (§D6). (This document says "adapter type" for this pair and keeps "kind" for the two publisher kinds, events and state, as ego-arch-003 does.)
- **Instance:** the existing `ID()` for publishers, unchanged. V6 keeps requiring it to be unique per publisher kind (events, state). Stores, encryptors and resolvers have no instance identity and do not need one: each sits in a single-value slot, and the slot's field name already identifies it.

An adapter that does not implement `Describer` is **undeclared**. It keeps working exactly as today. Inspection reports it as `(Descriptor{}, false)`, and the composition root names it by its slot, as `compose/goakt` already does (`compose/goakt/app.go:246-257`). A nil or typed-nil value is undeclared too, whatever its type's method set: `Describe`, `StarterOf` and `PingerOf` report it as absent and never call its methods.

The fact that every publisher's `ID()` is a type-wide constant (`"ego-kafka"`, `kafka.go:84-86`) is a real limitation, but fixing it changes publisher configuration, not the SPI; decision O3 (§9) adds an optional `ID` to each publisher `Config` (follow-up F-C).

### D3 — Capabilities: declared, implemented, inspected in one place

#106 asks to tell apart a mandatory, an optional and an unsupported capability:

| Class | Meaning | How it is expressed |
|---|---|---|
| Mandatory | Every implementation of the port has it | The port interface's own method set; the compiler enforces it. Example: `Ping` on every store (`persistence/events_store.go:112`). A mandatory capability is **implied by the port**: it is never declared, and V8b ignores it. So `CapReady` is implied for every store port and optional for publishers |
| Optional, supported | This implementation has it | Declared in `Descriptor.Capabilities` **and** implemented through the optional interface the owning contract package declares |
| Unsupported | This implementation does not have it | Not declared and not implemented |

**Where a port and a capability are defined.** In the contract package that owns the port. Each contract package declares its port names (for example `publishing.PortEventPublisher`, `persistence.PortEventsStore`), and each optional capability as a constant next to its optional interface, plus one accessor. These are **untyped string constants**, which convert to `adapter.Port` and `adapter.Capability` where they are used, so no existing contract package has to import `port/adapter`. That matters twice: contracts stay independent of each other, and moving `port/publishing` into the ego-arch-006 contracts module cannot create a module cycle through `port/adapter`, whichever module `port/adapter` sits in (under decision O2 it joins the contracts module in ego-arch-006 S2). For the one adapter capability core uses today:

```go
package tenancy

// CapFixedTenant means "implements FixedTenantResolver". It says the resolver
// can be asked for a fixed tenant, not that it has one: FixedTenant may still
// report (zero, false) at run time (tenancy/resolver.go:100-111).
const CapFixedTenant = "tenancy.fixed-tenant" // untyped; converts to adapter.Capability

// AsFixedTenantResolver returns r as a FixedTenantResolver and true when r
// implements it. It is the only type assertion on FixedTenantResolver.
func AsFixedTenantResolver(r TenantResolver) (FixedTenantResolver, bool)

// FixedTenantOf returns r's fixed tenant when r implements FixedTenantResolver
// and reports one. It calls AsFixedTenantResolver; it asserts nothing itself.
func FixedTenantOf(r TenantResolver) (TenantID, bool)
```

**What the capability means.** The contract already allows a multi-tenant resolver to implement `FixedTenantResolver` and return `(zero TenantID, false)` (`tenancy/resolver.go:100-111`). So `CapFixedTenant` is defined as the *interface* ("can be asked"), which is static and can be checked at composition time, not as the *answer* ("has a fixed tenant"), which only the call can tell. V8b checks that the declaration matches the interface through `tenancy.AsFixedTenantResolver`. The engine asks for the answer through `tenancy.FixedTenantOf`.

`engine.go:883` then calls `tenancy.FixedTenantOf(engine.tenantResolver)` instead of asserting the interface itself. The behavior does not change; the assertion moves into the contract that defines it, where every future caller will find it. That is the concrete meaning of "no scattered type assertions": exactly one assertion per optional interface, in the package that owns it (`tenancy.AsFixedTenantResolver`; `adapter.Describe`, `StarterOf`, `PingerOf`). Every other site calls those functions.

**Why both a declaration and a method.** The method is what the code calls; the declaration is what can be inspected and validated before anything runs. Either one alone fails a criterion: a method alone is invisible until the first call (today's state), and a declaration alone can claim something the adapter cannot do. Keeping both means they can disagree, so two checks keep them honest: V8 at composition time (§D6) and the conformance suite in the adapter's own tests (§D8).

**Truth for undeclared adapters.** For an adapter without a descriptor, the accessor still works from the method set, exactly as today, so existing resolvers keep their fixed-tenant behavior without a code change. Declaring is how an adapter becomes inspectable; it is never required to keep working in v4.

**Initial vocabulary.** Only capabilities with a caller on `main` are defined in this change: `adapter.CapStart`, `adapter.CapReady` and `tenancy.CapFixedTenant`. `logger.go:125` and `:334` assert `kitlog.CallerSkipper` and `kitlog.ManagedLogger`, but those are optional interfaces of the separate `kit-logger` module and the logger is not an Ego port; they stay as they are until #31 defines an observability contract. The `port/behavior` envelope assertions (`event_sourced_actor.go:773`, `durable_state_actor.go:502`) are behaviors written by the domain author, not adapters, and are out of scope. A capability that #24 or #11 needs later (for example a publisher that can flush) is added by that issue with its semantics; this design reserves no name without semantics.

### D4 — Lifecycle and ownership contract

The composition-level rules stay those of ego-arch-003 §D5–§D7. This decision states the adapter-level half. Each rule names the conformance check that proves it (§D8).

| Rule | Applies to | Contract | Why | Check |
|---|---|---|---|---|
| L1 | adapters implementing `Starter` | `Start` either succeeds or releases whatever it acquired before returning its error | `compose/internal/lifecycle` never undoes the step that failed (`lifecycle.go:60-63`); an adapter that leaks on a failed `Start` leaks for good | AT-2 |
| L2 | owned adapters (`Close`) | `Close` is idempotent, safe on a value that was never started, and safe after a failed `Start` | `compose/goakt` closes unattached publishers from `releasePublishers` on every failure path (`app.go:406-425`) and attached ones through `Engine.Stop` (`engine.go:464-501`); a second close must not fail or panic | AT-3 |
| L3 | owned and borrowed (`Close`, `Disconnect`) | Return by the caller's context deadline | Cleanup runs under one context bounded by `ShutdownTimeout` for all steps together (`lifecycle.go:79-83`, `:272-293`); one adapter that ignores it consumes everyone's budget. `kafka.go:76` is the counter-example today | AT-4 |
| L4 | adapters implementing `Pinger` | `Ping` answers "ready to serve now". It may establish a connection, as the store contracts document (`persistence/events_store.go:109-112`); a connection it opens on a borrowed adapter stays the consumer's to close | Keeps ego-arch-003 §D5 ("App only pings them") literally true while admitting what `testkit/eventstore.go:273-276` does | AT-5 |
| L5 | owned adapters | After `Close`, operations fail with the port's documented error (`publishing.ErrPublisherNotStarted` for publishers) and never block | Today's publisher contract (`publisher/kafka/publisher_contract_test.go:56-68`), generalized | PT-1 |
| L6 | new adapters | A constructor should do no I/O; I/O belongs in `Start` | Lets `New` stay I/O-free end to end (ego-arch-003 §D4a) and makes rollback of a failed dial the composition root's job, not the consumer's. Existing publishers dial in their constructor (`kafka.go:58-70`); O5 (maintainer decision, 2026-09-27) keeps them; the rule applies to new adapters | review |

**Where Start and Ready happen in the composition root.** Inside the existing step 4, "attach publishers" (`compose/goakt/app.go:174`, `:355-369`). For each publisher, in `Spec` order, step 4 calls `Start` when `adapter.StarterOf` finds one, then `Ping` when `adapter.PingerOf` finds one, then attaches each publisher kind as it does today. The start-and-probe loop lives in a runtime-free helper, `compose/internal/adapters` (covered by `composition-no-runtime` like `compose/internal/lifecycle`), so `compose/inmem` (#148) reuses it unchanged instead of copying it. Borrowed stores keep being pinged in step 1. No new step is added, so `StartError.Step` values and the D6 table keep their meaning; the step's error names the publisher by `ID()` and, when declared, by `Descriptor.Name`.

**Partial failure.** If publisher *k* fails `Start` or `Ping`, step 4 fails, the lifecycle undoes steps 3 down to 1, and `releasePublishers` closes every publisher that was not attached, started or not. L1 guarantees publisher *k* cleaned up after itself, and L2 makes closing the others safe whether or not they were started. The ownership rule for the consumer stays the one ego-arch-003 §D5 states: once `New` succeeded, call `Stop` and never close a publisher yourself.

**Stop.** Unchanged from ego-arch-003 §D7: projections, then `Engine.Stop` (closes publishers and the event stream), then the actor system. Borrowed adapters are never closed. The known gap (anything an actor emits after publishers close is not published) stays #24's (§7).

**Restart.** An adapter is not required to support `Start` after `Close`. The `App` is single-use (`lifecycle.go:124-129`); restart semantics belong to #24 (LIFE-008).

### D5 — `compose/internal/lifecycle` stays internal (maintainer decision O4, 2026-09-27)

ego-arch-003 §9 left this to #106. The maintainers decided on 2026-09-27 to keep it internal (O4). Adapters implement `Start`, `Close` and `Ping`; they never sequence other components, so they need the contract in §D4, not the sequencer. Making the sequencer public would create an API with one kind of caller (composition roots, all under `compose/`), and #24's LIFE-001 state machine may still reshape it.

### D6 — Validation at composition: rule V8

`compose.Spec.Validate` gains one rule, reported as a `*compose.ValidationError` with `Rule: "V8"` like the others (`compose/errors.go:30-50`):

- **V8a** — an adapter that declares a descriptor must list the port of the slot it sits in among its `Ports` (a membership check, `Descriptor.Serves`). A value placed in `Spec.StateStore` whose descriptor lists only `persistence.EventsStore` is a wiring mistake. A value that lists both `EventsStore` and `SnapshotStore` is valid in either slot.
- **V8b** — declaration and implementation agree **in both directions** for every optional capability `compose` knows for that slot's port: declared ⇒ implemented, and implemented ⇒ declared. In this change that is `CapStart` and `CapReady` for publishers, checked through `adapter.StarterOf`/`PingerOf`, and `CapFixedTenant` for the tenant resolver, checked through `tenancy.AsFixedTenantResolver`. So a declared resolver that implements `FixedTenantResolver` without declaring it fails V8b; it does not pass silently. Capabilities implied by the port (`CapReady` on stores) are skipped. A declared capability that `compose` does not know (one added later by another issue) is accepted here and checked by the adapter's own conformance tests (AT-1, through `Target.Capabilities`). A mismatch names the slot, the adapter type and the capability.
- **V8c** — a slot's required capabilities are met. **In v4 no slot requires an optional capability**, so V8c starts empty. It exists so #11 (runtime negotiation) and #24 (for example, a drain policy that needs publishers able to flush) can add a requirement as a table entry with a test, instead of a type assertion at the point of use.

V8 runs only on adapters that declare a descriptor, so every `Spec` that validates today still validates. That is deliberate for v4 compatibility: an undeclared resolver that implements `FixedTenantResolver` keeps working as today and is reported by inspection as undeclared. O7 asks whether #124 makes declaring mandatory.

**Ordering.** V8 runs after V5 and V6 and skips every slot value they already rejected (a typed nil, a nil publisher, a duplicate publisher ID). It runs value by value, right after V5 and V6 accepted that value, so its problems keep `Validate`'s Spec field order. Calling `Describe` on a typed-nil pointer can dereference nil and panic, and one problem should produce one error. It is static: it inspects values the consumer already placed in named fields, with no I/O. Its only reflection is nilness: `compose`'s existing typed-nil check (ego-arch-003 §D2 allows exactly that one use in `compose`), and the nilness check (`isNil`) inside `port/adapter`'s accessors, which inspects whether a value is nil and never discovers methods.

`compose` imports `port/adapter` for V8. That is a contract import, which `composition-no-runtime` allows (`internal/cmd/archcheck/rules/rules.go:202-214`).

### D7 — Adapters must not depend on the composition root

**Decision (maintainer decision O1, 2026-09-27): no.** A nested adapter module must not import `compose` or anything under `compose/`, in production code or in tests.

Why:

1. **Direction.** The composition root depends on adapters, because the consumer's `main` hands them to it; an adapter depending on the composition root inverts that. ego-arch-001 §3 already says an external adapter "MAY import only contract packages and `egopb`". `compose` is not a contract. The rule only makes enforceable what the ADR already states.
2. **Measured cost.** Importing `compose/goakt` from `publisher/kafka` brings 45 GoAkt packages back into its production build, and importing the neutral `compose` pulls six root-level contracts that stay in the root module until ego-arch-006 F1 (exploration §6). After ego-arch-006 S3 either import forces the publisher to require the root module again.
3. **No legitimate need.** The obvious use, a helper such as `kafka.Register(spec *compose.Spec)`, is a form of self-registration that ego-arch-003 §D2 forbids; the consumer writes `spec.EventPublishers = append(spec.EventPublishers, p)` instead. End-to-end tests that need a running `App` belong in an unreleased integration module such as `test/compat` (ego-arch-006 D5).

**The rule: `external-adapter-no-composition`.** Added to `DefaultRules` in `internal/cmd/archcheck/rules/rules.go`:

| Field | Value |
|---|---|
| ID | `external-adapter-no-composition` |
| Description | nested adapter modules must not import the composition root (`compose` or anything under it) |
| Source | `ego-arch-004/design.md §D7` |
| Layer | `ExternalAdapterLayer` (`internal/cmd/archcheck/rules/layers.go:121-131`), the same layer `external-adapter-no-runtime` uses: every package of a nested module under `publisher/` |
| Semantics | denylist |
| Forbids | `isCompositionImport(rootModulePath, importPath)` (`layers.go:157-159`), the predicate `composition-leaf` already uses: `<root>/compose` or any path under it, matched by whole path segment |
| Reason | names the composition package imported and says adapters may import only contracts and `egopb` (ego-arch-001 §3) |

**Fail-closed behavior.**

- The layer is shared with `external-adapter-no-runtime`. If the publisher modules move or the root module path is misread, the layer matches zero packages and `Evaluate` fails the run (`internal/cmd/archcheck/rules/evaluate.go:211-217`) instead of passing vacuously.
- The composition prefix is built from the root module path read from `go.mod` (`DefaultRules(rootModulePath)`), so the rule follows the ego-arch-006 D1 path migration without an edit.
- The nested-module loader parses every non-test file and ignores build tags (`internal/cmd/archcheck/loader.go:240-277`), so it can over-report an import but never miss one in a production file.
- **No `main` or example exemption inside an adapter module**, unlike `composition-leaf`. The harm is to the adapter module's own `go.mod`, which a `main` package or an example inside that module damages just as much. A demo program belongs in its own unreleased module.
- **Tests.** archcheck does not read `_test.go` files. The test side is covered by each publisher's closure test (`publisher/kafka/closure_test.go:59-75`), which slice SPI-2 extends to also reject `<root>/compose` and anything under it. `compose/goakt` is already caught today through the GoAkt check.

**Interaction with the other rules.**

- `composition-leaf` covers root-module production packages (`layers.go:189-206`); this rule covers nested adapter modules. The layers do not overlap, and both use the same predicate, so together they say: only packages under `compose/`, root-module `main` packages, examples, tests, and unreleased consumer modules (`benchmark`, `example/cluster`, `test/compat`) may import the composition root.
- `no-cross-module-internal` (`modules.go:86-100`) already rejects an adapter importing `compose/internal/...`, and Go's own `internal` rule rejects it too. For that path, both archcheck rules report the same edge. That duplication is accepted: each report names a different broken constraint, and no baseline entry is needed because no such import exists.
- `external-adapter-no-runtime` is not widened. Its ID and description say "runtime", and a violation must name the rule it actually broke.
- **New adapter families.** `ExternalAdapterLayer` matches only `publisher/`. When the first adapter module outside it appears (a store or telemetry adapter), its directory root is added to that one layer, and both adapter rules follow. Where such modules live is decided when the first one arrives (O6).

### D8 — Conformance suite

Two new packages, both standard-library-only so they pass `contract-allowlist` and the publishers' closure tests (exploration §5):

| Package | Tests | Used by |
|---|---|---|
| `port/adapter/adaptertest` | Lifecycle and descriptor rules any adapter must meet: **AT-1** a declared descriptor is stable across calls and its `Ports` include the one the caller expects; declared capabilities and implemented interfaces agree in both directions (V8b, in the adapter's own tests). `adaptertest` knows only `CapStart` and `CapReady` itself; any other capability is checked through `Target.Capabilities`, which the adapter's test fills (§D8 "Target"). **AT-2** a failed acquire (`Start`, or `Connect` for a borrowed adapter) releases resources (L1). It is driven by a failure hook the caller supplies. **AT-3** release twice, release without acquire, and release after a failed acquire (L2). **AT-4** release returns within a short deadline while the backend is stalled (L3). It is driven by a caller-supplied `Stall` hook; without the hook AT-4 cannot fail, so it is reported as not exercised (§D8 "Hooks"). **AT-5** `Ping` after acquire succeeds for a reachable adapter (L4) | any adapter: publishers, stores, future adapters |
| `port/publishing/publishingtest` | Publisher-specific rules: **PT-1** `Publish` after `Close` returns `publishing.ErrPublisherNotStarted` (L5, generalizing today's four `publisher_contract_test.go` copies); **PT-2** `ID()` is non-empty and stable; **PT-3** a published event reaches the caller-supplied observer | the four publishers |

Stores keep `persistence/conformance` for their data semantics. It is canonical (EGO-TENANT-003), and #106 says not to redesign existing persistence contracts without evidence. They add `adaptertest` for lifecycle only.

**How the suites are called.** Same shape as `persistence/conformance`: the adapter's own test passes a factory that returns a fresh value per check.

**Skip only on a declared "unreachable".** A check is skipped only when the factory returns an error matching the sentinel `adaptertest.ErrUnreachable` (`errors.Is`). Any other factory error fails the check. This narrows the rule of `persistence/conformance/check.go:79-81`, which skips on *any* `Connect` error, so a misconfigured adapter can no longer hide behind a skip. The two CI adopters (listed below) must run **unskipped**: the slice checks that their `-v` output has no `SKIP` for a conformance subtest.

**Target: what the suite needs to know.** Owned and borrowed adapters are acquired and released by different methods (publishers: `Start` if present, then `Close`; stores: `Connect`, then `Disconnect`), so the caller says which one it is:

```go
type Target struct {
	Port      adapter.Port                  // the slot port under test; must be in Descriptor.Ports
	Ownership Ownership                     // Owned: acquire = Start (if StarterOf), release = Close
	                                        // Borrowed: acquire = Connect, release = Disconnect
	New       func(t *testing.T) (any, error) // fresh value; wrap ErrUnreachable to skip
	FailStart func(t *testing.T) (any, error) // optional: a value whose separate acquire fails (AT-2)
	// Capabilities maps each port-specific capability to its implements-check,
	// e.g. {tenancy.CapFixedTenant: func(v any) bool { _, ok := tenancy.AsFixedTenantResolver(v.(tenancy.TenantResolver)); return ok }}.
	Capabilities map[adapter.Capability]func(v any) bool
	Stall     func(t *testing.T)              // optional: make the backend stop answering (AT-4)
}
```

`adaptertest` finds `Close`, `Connect` and `Disconnect` through small structural interfaces it owns, and finds `Start`/`Ping` through `adapter.StarterOf`/`PingerOf`.

**Hooks.** `FailStart` and `Stall` exist because a suite cannot make a real backend fail or hang by itself. Without `FailStart`, AT-2 is not exercised; without `Stall`, AT-4 is not exercised. Each is logged as "not exercised: no hook", never as passed. `FailStart` only makes sense for an adapter whose acquire is separate from its constructor (a `Starter`, or a borrowed adapter with `Connect`). A publisher that dials in its constructor, as all four do today and O5 keeps, has no value left after a failed dial, so for it AT-2 and the "release after a failed acquire" case of AT-3 are reported as "not exercised: acquire happens in the constructor", the same way a missing hook is. The websocket adopter therefore supplies only `Stall`, through an `httptest` handler that stops reading; its AT-2 would only become exercisable if it gained a `Start`, which O5 does not plan. The in-memory `testkit` stores have no backend that can stall, so AT-4 is not exercised for them; the conformance summary lists that explicitly.

**Capability checks beyond `CapStart`/`CapReady`.** `adaptertest` imports only the standard library and `port/adapter`, so it cannot check `tenancy.CapFixedTenant` or any capability added later by itself. The adapter's own test supplies an implements-check per such capability in `Target.Capabilities`, built from the owning package's accessor (`tenancy.AsFixedTenantResolver` for `CapFixedTenant`). AT-1 then checks both directions for every capability in that map. A declared capability that is neither `CapStart`, `CapReady` nor in the map fails AT-1 with "no check supplied", so a new capability cannot pass unchecked. At composition time the same capability is checked by V8b in `compose`, which does import `tenancy`.

```go
// in publisher/websocket's own tests
func TestConformance(t *testing.T) {
	srv := httptest.NewServer(handler) // stdlib; allowed in an adapter module's tests
	defer srv.Close()
	adaptertest.Run(t, adaptertest.Target{
		Port:      publishing.PortEventPublisher,
		Ownership: adaptertest.Owned,
		New:       func(t *testing.T) (any, error) { return websocket.NewEventsPublisher(&websocket.Config{URL: wsURL(srv)}) },
		Stall:     stopReading(srv),
	})
	publishingtest.RunEvents(t, /* same factory, plus an observer on srv */)
}
```

**The two adopters #106 requires** (slice SPI-4):

- **A publisher:** `publisher/websocket`. It is the only publisher whose tests can build a real instance in CI with the standard library alone (an `httptest` server), since the others need a broker. Kafka, NATS and Pulsar adopt in follow-up F-A, skipping through `ErrUnreachable` where no broker is reachable. Today the websocket publisher breaks L2 (exploration §2), so SPI-4 fixes its `Close` first.
- **A store:** the `testkit` in-memory `EventStore`, `DurableStore` and `OffsetStore`, which already run `persistence/conformance` (`testkit/conformance_test.go:47-59`) and add `adaptertest`.

Neither needs a special case in core. The composition root probes stores through `adapter.PingerOf` instead of its private `pinger` (`compose/goakt/app.go:241`), starts publishers through `adapter.StarterOf`, and validates both through V8.

**How nested modules run the suites without the root runtime.** `adaptertest` imports only the standard library and `port/adapter`; `publishingtest` imports only the standard library, `port/publishing` and `egopb`, and deliberately not `port/adapter`, so it can move into the ego-arch-006 contracts module with `port/publishing` without creating a module cycle. A publisher's test closure therefore gains no GoAkt package and not the root package `ego`, so `TestUnitTestClosureExcludesRuntimeAndRoot` stays green. Today the publishers resolve these packages through their existing requirement on the root module (`publisher/kafka/go.mod:7`, `:50`). After ego-arch-006 S3 they require only the contracts module; `port/publishing/publishingtest` moves with `port/publishing` automatically, and under maintainer decision O2 (§9) `port/adapter` and `port/adapter/adaptertest` join that module in ego-arch-006 S2, amending D7 (i).

A nested *store* module (none exists yet) that wants `persistence/conformance` must still require the root module, because `persistence` and `testkit` stay there until ego-arch-006 F1/F2. That is already recorded in ego-arch-006 §2.2 and is not changed here.

### D9 — Compatibility and apidiff

Everything is additive in v4, per ego-arch-001 §10.

| Package | Change | apidiff expectation |
|---|---|---|
| `port/adapter`, `port/adapter/adaptertest`, `port/publishing/publishingtest` | new | additions only |
| every existing contract interface (`port/publishing`, `persistence`, `offsetstore`, `encryption`, `tenancy`) | no method added to any interface | no incompatible change |
| `tenancy`, `encryption`, `persistence`, `offsetstore`, `port/publishing` (port-name constants only) | adds untyped constants; `tenancy` also adds `AsFixedTenantResolver` and `FixedTenantOf` | additions only; no new import |
| `compose` | adds rule V8 inside `Validate` | no exported change; behavior changes only for adapters that declare a descriptor, which none do before this change |
| `compose/goakt` | step 4 calls `Start`/`Ping` when implemented | no exported change |
| `ego` | `engine.go:883` calls `tenancy.FixedTenantOf` | no exported change |
| `testkit`, `publisher/websocket` | add `Describe` methods (no `Start`: O5 keeps constructor dialing) | additions only |

**No deprecation is needed in v4.** Nothing is replaced; the optional interfaces keep working for undeclared adapters. Whether the next major (#124) makes `Describe` a required method on each port is a question for #124 and #37, recorded in §9 and not decided here.

**Required check per slice:** `apidiff` (`golang.org/x/exp/cmd/apidiff`) between the baseline and the slice head for every package the slice touches, recorded in the pull request, as ego-arch-001 §5 does for S1.

## 4. Extension guide outline

Slice SPI-5 writes `docs/adapters.md` from this outline. Its promise: a new adapter is added without editing any file outside its own module, except one line in archcheck when it opens a new adapter family.

1. **Pick the port.** Find the contract package (`persistence`, `offsetstore`, `port/publishing`, `encryption`, `tenancy`). If none fits, the adapter needs a new contract under `port/`, which is an ADR change, not an adapter.
2. **Create the module.** Put it in its own directory with its own `go.mod`, under the family's adapter root (`publisher/` today; for a new family the root is decided when its first module arrives, O6). Import only contract packages and `egopb`; never `ego`, GoAkt or `compose/...` (archcheck rules `external-adapter-no-runtime`, `external-adapter-no-composition`). Copy `closure_test.go` from an existing publisher.
3. **Implement the port,** plus a `var _ port.Interface = (*T)(nil)` assertion.
4. **Declare the descriptor.** Implement `Describe()` with every port the type implements, a name, and every optional capability you implement; leave out capabilities the port already implies. Declare exactly what you implement: V8 and `adaptertest` both fail on a mismatch in either direction.
5. **Lifecycle.** Owned adapter: do I/O in `Start`, clean up after a failed `Start`, make `Close` idempotent and deadline-bound. Borrowed adapter: `Connect`/`Disconnect`/`Ping` as the port documents; the composition root never connects or closes it.
6. **Run the conformance suites** from your tests: `adaptertest` always, plus the port's own suite (`publishingtest`, `persistence/conformance`). Fill `Target` (ownership, and the `FailStart`/`Stall` hooks if you can). Return `adaptertest.ErrUnreachable` only when the backing service is genuinely absent; any other error fails.
7. **Wire it.** Show the consumer's `compose.Spec` field and the ownership rule (publishers: never close after `New`; stores: connect before `New`, disconnect after `Stop`).
8. **CI.** Nothing to register: `ciselect` discovers the module from its `go.mod`. List it in `docs/ci.md` if it is released.
9. **Checklist** for the pull request: archcheck green, closure test green, conformance green (skipped only through `ErrUnreachable`, with the reason), apidiff additions only.

## 5. Chain of specs

Per the maintainer decision of 2026-09-27 (decision A in §9), no spec carries more than five atomic tasks. The work is split into a chain of three specs. This change directory stays the umbrella: exploration, proposal and this design hold the SPI model, the decisions and the whole picture.

**Layout.** The three specs live in `openspec/changes/ego-arch-004/specs/<name>/spec.md`. They reuse the `specs/` subfolder convention that `ego-store-001`, `ego-tenant-002`, `ego-tenant-003` and `ego-write-004` already use. This is the lightest option: no separate change directories (`ego-arch-004a/b/c`) and no copied proposal. Unlike those changes, each spec file also carries its own tasks, checks, file ownership, dependencies and a "next in the chain" link, instead of a shared `tasks.md`, so every spec can be picked up and verified on its own. **The spec files are the authoritative task lists; this section only summarizes them.**

| Order | Spec | Slices (pull requests) | Tasks | Depends on | Next |
|---|---|---|---|---|---|
| 1 | [`specs/adapter-spi-boundary/spec.md`](./specs/adapter-spi-boundary/spec.md): `port/adapter`, untyped port-name constants, archcheck `external-adapter-no-composition`, publisher closure tests, the `ego-arch-001/design.md:118` amendment | SPI-1, SPI-2 | 5 | nothing | spec 2 |
| 2 | [`specs/adapter-conformance/spec.md`](./specs/adapter-conformance/spec.md): `adaptertest`, `publishingtest`, idempotent websocket `Close`, websocket and `testkit` adopters | SPI-3, SPI-4 | 5 | spec 1; if ego-arch-006 S3 lands first, the websocket half waits for S2 to carry `port/adapter` (O2) | spec 3 |
| 3 | [`specs/adapter-composition/spec.md`](./specs/adapter-composition/spec.md): V8, the step-4 start-and-probe helper in `compose/internal/adapters`, `adapter.PingerOf` in `probeStores`, `tenancy.AsFixedTenantResolver`/`FixedTenantOf`, `docs/adapters.md` | SPI-5 | 5 | specs 1 and 2; rebase after #147 S4-4 (§6) | none; follow-ups below |

The slice names SPI-1 to SPI-5 used elsewhere in this document refer to the pull requests inside these specs. File ownership never overlaps between specs: in particular, spec 1 owns all four `publisher/*/closure_test.go` files, and spec 2 does not edit them. The ~400 changed lines per pull request is a planning heuristic, not a cap.

### Named follow-ups (outside this change)

- **F-A** Kafka, NATS and Pulsar adopt `Describe` and the suites (skipped through `ErrUnreachable` without a broker; Pulsar through its existing testcontainers setup). Fixes `kafka.go:76` under L3 and makes each `Close` idempotent under L2: NATS calls `Drain` again on an already closed connection (`nats.go:119-125`, `:235-241`), and a second Close for Kafka and Pulsar is unverified (exploration §2).
- **F-B** `port/adapter` and `adaptertest` move into the ego-arch-006 contracts module in its slice S2 (decision O2; recorded amendment of ego-arch-006 D7 (i), §9).
- **F-C** An optional `ID` in each publisher `Config`, defaulting to today's constant (decision O3). A good fit to land together with F-A, one publisher at a time.
- **F-D** (not scheduled) an `external-adapter-contracts-only` allowlist that would enforce all of ego-arch-001 §3, only if the maintainers later ask for it; O1 chose the dedicated denylist rule.
- **F-E** Provider-side runtime capabilities for #11 RUNTIME-006: a runtime adapter's `Descriptor` declares which `port/runtime` capabilities from #147 it supports. These capabilities are declaration-only: because the composite interface in [`openspec/changes/ego-runtime-001/design.md`](../ego-runtime-001/design.md) §8 makes every runtime implement every capability interface, V8b must not apply the implemented ⇒ declared direction to the runtime port. #148's typed "unsupported" error stays the call-time answer (§2).
- **F-F** Observability port for telemetry and logging (#31), after which the `kit-logger` assertions can move behind it.

## 6. Dependencies and sequencing

- **Nothing blocks SPI-1, SPI-2 or SPI-3.** They touch only new packages, archcheck and tests.
- **ego-arch-006 S2/S3 (#102) and F4.** At the baseline, publishers still require the root module, so SPI-4 works as designed. Under maintainer decision O2 (§9), ego-arch-006 S2 also carries `port/adapter` and `adaptertest`, which amends its approved D7 (i). If S3 lands before S2 carries them, the publisher half of SPI-4 waits for that.
- **ego-arch-006 D1 (module path migration).** Direction approved 2026-09-27, execution pending confirmation (ego-arch-006 §3). The new packages have no special path handling; the single D1 pull request renames them with everything else. Avoid running a slice and the D1 pull request in parallel on the same files.
- **#123 and #105 IMPL-5/IMPL-6.** No dependency. SPI-5 edits `compose/goakt/app.go` step 4 and `probeStores`, which IMPL-5 (example migration) does not touch. #148 (`compose/inmem`, IMPL-6) reuses `compose/internal/adapters` from SPI-5; if #148 lands first, SPI-5 extracts the helper from both composition roots instead.
- **Hot-spot files shared with other open work.** No slice waits for these issues, but the same files change, so each pull request rebases on whichever lands first:

  | File | This change | Other work |
  |---|---|---|
  | `engine.go` | SPI-5: one line at `:883` | #147 S4-2 edits only the error `var` block (`engine.go:61-174`, ego-runtime-001 §9), a different region; S4-3 puts its compile-time assertion in its own file, `engine_runtime.go`, and does not touch `engine.go` |
  | `compose/goakt/app.go` | SPI-5: step 4 and `probeStores` | #147 S4-4 (adds `App.Runtime()`, a different function); #146 (two-node test through `compose/goakt`; mostly new test files, but it may touch `WithCluster` wiring) |
  | `compose/goakt/app_test.go` | SPI-5: step-4 failure tests | #146 and #147 S4-4 add tests beside them |
  | `publisher/*/closure_test.go` | SPI-2 only (SPI-4 does not edit them) | — |

  The only overlap that needs ordering is `compose/goakt/app.go` with #147 S4-4. The SPI-5 edits there are small and local, so the recommended order is to let S4-4 land first and rebase SPI-5 onto it. `engine.go` is also touched by #147 S4-2, but in a different region, so an ordinary rebase suffices.
- **#24.** See §7. No slice waits for #24.

## 7. Interactions with #24 (lifecycle epic)

| #24 item | Interaction | Stays with #24 |
|---|---|---|
| LIFE-001 state machine and ownership | §D4 states adapter-level guarantees under the ownership ego-arch-003 §D5 fixed | The framework-wide state machine; whether `lifecycle` is reshaped (O4) |
| LIFE-002 dependency startup ordering | Owned adapters start inside step 4, in `Spec` order | Any ordering beyond D6's five steps |
| LIFE-003 stop admitting commands | None; admission still closes at `Engine.Stop` (ego-arch-003 §D7) | The engine-level admission switch |
| LIFE-004 flush pending work | None. A future "flush" capability is added by #24 with its semantics, through V8c | The drain policy and the dropped-emissions question of ego-arch-003 §D7 |
| LIFE-006 runtime/store ownership transfer | Borrowed stays borrowed | Whether stores can be handed over |
| LIFE-007 timeouts | L3 makes every adapter honor the shared cleanup deadline | The default `ShutdownTimeout` (ego-arch-003 §9) |
| LIFE-008 restart | Adapters need not restart after `Close` | Restart semantics |
| "Startup failure identifies the responsible component" | Step 4 errors name the publisher by `ID()` and descriptor name | — |

## 8. Acceptance mapping for #106

| #106 criterion | Where | Proof |
|---|---|---|
| Public SPI does not depend on concrete implementations | SPI-1 | `port/adapter` architecture test (stdlib only) and `contract-allowlist` |
| Capabilities are explicit and inspectable without scattered type assertions | SPI-1, SPI-5 | `adapter.Describe`/`StarterOf`/`PingerOf` and one assertion per optional interface in its owning contract (`tenancy.AsFixedTenantResolver`); `engine.go:883` moved behind `tenancy.FixedTenantOf`; V8 |
| Lifecycle and ownership are documented | §D4, SPI-5 guide | L1–L6 and the ego-arch-003 §D5 table, linked from `docs/adapters.md` |
| Partial failures clean up started resources | SPI-3, SPI-5 | AT-2/AT-3; step-4 injected-failure test in `compose/goakt` |
| A minimal reusable conformance suite exists | SPI-3 | `adaptertest`, `publishingtest`, with self-checks |
| At least two adapter types use the model without special conditions in core | SPI-4, SPI-5 | `publisher/websocket` and `testkit` stores; `compose/goakt` uses only `port/adapter` interfaces |
| The guide explains how to add an adapter without modifying core | SPI-5 | `docs/adapters.md` |
| (comment) Adapters and the composition root | SPI-2 | `external-adapter-no-composition` plus the closure-test extension |

## 9. Maintainer decisions (2026-09-27)

The maintainers made these decisions on 2026-09-27 on PR #149. They replace the open-decision list this section held before. The options that were considered are kept below each decision, so the reasoning stays reviewable.

**A — Chained specs.** No spec may exceed 4–5 atomic tasks. `ego-arch-004` stays the umbrella (exploration, proposal, design). The chain is spec 1 = SPI-1 + SPI-2, spec 2 = SPI-3 + SPI-4, spec 3 = SPI-5. Each spec lists its own tasks, checks, dependencies, file ownership and next spec. §5 describes the layout chosen.

**B — The former open decisions.** O1–O6 were approved as recommended; O7 was deferred.

| # | Question | Decision | Options that were considered |
|---|---|---|---|
| O1 | May nested adapter modules import `compose` or `compose/...`? | **No**, enforced by the dedicated denylist rule `external-adapter-no-composition` (§D7, spec 1). The allowlist variant stays a possible later follow-up (F-D), not scheduled | (b) allow neutral `compose` only, which still breaks the publishers after ego-arch-006 S3 (exploration §6); (c) the allowlist `external-adapter-contracts-only` that enforces all of ego-arch-001 §3; (d) allow and document |
| O2 | Where `port/adapter` and `adaptertest` live once publishers require only the contracts module | **In the contracts module, added in ego-arch-006 S2.** Both packages are standard-library-only, so no module cycle is possible | (b) a separate SPI module, which fails ego-arch-006 §6(2); (c) publishers adopt only after F1 |
| O3 | Publisher instance identity (every `ID()` is a type-wide constant, `kafka.go:84-86`) | **An optional `ID` in each publisher `Config`, defaulting to today's constant** (additive), as follow-up F-C | (b) one publisher per type, documented; (c) derive the ID from the topic or URL |
| O4 | Promote `compose/internal/lifecycle` (left to #106 by ego-arch-003 §9) | **Keep it internal** (§D5) | (b) promote now |
| O5 | Constructors that do I/O (all four publishers dial in `New*`) | **Keep the existing constructor-dialing publishers; new adapters do their I/O in `Start`** (L6) | (b) lazy constructors plus `Start`, deprecating the dialing ones until #124 |
| O6 | Directory convention for adapter modules outside `publisher/` | **Decide when the first non-publisher adapter module arrives.** Adding its root to `ExternalAdapterLayer` is a one-line change | (a) one root per family; (b) a single `adapter/<family>/<name>` root |
| O7 | Should the #124 major make `Describe` a required method of each port? | **Deferred to #124 and #37** (COMPAT-005) | (a) yes, at #124; (b) keep it optional |

**Recorded amendment to ego-arch-006.** Decision O2 amends ego-arch-006's approved D7 (i). The ego-arch-006 contracts module (slice S2) now holds `egopb`, `port/publishing` **and** `port/adapter` with `port/adapter/adaptertest`, instead of `egopb` and `port/publishing` alone. `port/publishing/publishingtest` goes with `port/publishing` in any case. The amendment keeps ego-arch-006's invariant that nothing in the contracts module imports the root: all four packages import only the standard library, `egopb` or each other. The owner of ego-arch-006 S2 applies it when that slice is planned; this pull request does not edit ego-arch-006's files.

## 10. Alternatives rejected

- **Add `Describe`, `Start` or `Ping` to the existing port interfaces.** It is the most direct, but it breaks every implementation outside the repository, and ego-arch-001 §10 decided "no break inside v4".
- **Capabilities only as optional interfaces, asserted where used** (today). Rejected: nothing can be inspected or validated before the first call, and the assertion sites multiply (exploration §3.1).
- **Capabilities only as a declared list, with no interface behind them.** Rejected: a flag can claim a behavior the adapter lacks, and the caller still needs a method to call, which means an assertion anyway.
- **One central capability registry in `port/adapter`** listing every capability of every port. Rejected: `port/adapter` would import or mirror every contract and change whenever any port does; the owning package already knows its capabilities.
- **Discovery by reflection** (scan an adapter's methods to build its descriptor). Rejected by ego-arch-003 §D2, which forbids reflection-based wiring, and it would make a descriptor impossible to review.
- **A plugin loader or a registry adapters add themselves to.** Out of scope for #106 ("dynamic loading", "remote registry"), and a registry is a service locator (ego-arch-003 §D2).
- **Widening `external-adapter-no-runtime` to also deny `compose`.** Rejected: its ID and description say "runtime", so a report would name the wrong constraint (§D7).
- **Widening `composition-leaf` to nested modules.** Rejected: `composition-leaf` exempts `main` packages and examples, which must not be exempt inside an adapter module, and it was scoped to the root module on purpose (ego-arch-003 §D8).
- **Putting the conformance suites in `testkit`.** Rejected: `testkit` is in the root module and imports persistence helpers; publishers would need the root module in their test builds, and after ego-arch-006 S3 they cannot have it.
- **Using `testify` in the new suites,** as `persistence/conformance` does. Rejected: everything under `port/` is a contract for `contract-allowlist`, and a carve-out would add a third-party requirement to the future contracts module for a convenience.

## 11. Evidence and reproduction

| Claim | How it was obtained |
|---|---|
| Every `file:line` in this document and in `exploration.md` | Read on `f2b5130` in a clean worktree |
| An adapter importing `compose`/`compose/goakt` passes archcheck, and the closure numbers in exploration §6 | `git archive f2b5130` into a scratch directory; add one file to `publisher/kafka` importing `compose` (and `compose/goakt`); `GOWORK=off go mod tidy`; `go run ./internal/cmd/archcheck`; `GOWORK=off go list -deps ./... \| wc -l` and `\| rg -c tochemey/goakt/v4`. Go 1.27.1 linux/amd64. Nothing from the spike is committed |
| Type-assertion inventory (exploration §3.1) | `rg -n '\.\((interface\|[a-z]+\.[A-Z]\w+\|\*?[A-Z]\w+)\)'` over production `.go` files, excluding tests, `mocks/`, generated code, examples and `benchmark`; then reading each hit |
