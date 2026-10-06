# Exploration — Canonical `TenantContext` and `TenantResolver` SPI (EGO-TENANT-001)

Date: 2026-09-13
Change: `ego-tenant-context`
Phase: `sdd-explore` (no code changed)
Repo: `/Users/pablogore/workspace/pablogore/ego` — module `github.com/pablogore/ego/v4` (fork of `github.com/tochemey/ego`, maintained independently)
Tracker: `getsyntegrity/ego#45` (EGO-TENANT-001), parent epic `getsyntegrity/ego#23` (EGO-TENANT), dependency `getsyntegrity/ego#10` (EGO-ARCH hexagonalization)

All claims below are backed by `path:line` evidence gathered directly from this checkout. Claims that could not be verified from source (mostly GoAkt v4 internals not present in the local module cache) are marked `UNVERIFIED`.

---

## 0. Headline

Four findings reframe the request:

1. **This is genuinely greenfield — with one important caveat.** A repo-wide scan for `tenant` and for adjacent concepts that could act as a de facto tenant (`org`, `account`, `workspace`, `namespace`, `realm`) turns up zero real hits (§3). But the codebase has an extremely strong, consistent identity idiom already in place — every ID in the system (`persistenceID`, `entityID`, `sagaID`, `projection name`, `keyID`) is a **bare `string`**, never a wrapper type (§4.1). `TenantID` will either follow that idiom exactly, or be the first typed identifier in the framework — that is a real design fork, not a detail.

2. **Epic #10 (hexagonalization) is not "aspirational" — it is a real, working, but narrow enforcement mechanism that this change would need to extend.** `logger_architecture_test.go` (§2) is a **substring-scan** over first-party `.go` files via `filepath.WalkDir` + `strings.Contains`, banning specific import strings and API calls (`"github.com/tochemey/goakt/v4/log"`, `slog.*`, `fmt.Printf`, etc.), with one file (`logger.go`) exempted as "the seam." It is not an AST walk, not `go list -deps`, and it enforces exactly one axis (logging) for exactly one axis of leakage (banned strings, banned outside one file). The core package tree today is **not** import-clean in general — `engine.go`, `option.go`, `persistence/*.go` etc. freely import GoAkt, OTel, and protobuf types; nothing prevents that. #45's acceptance criterion "architecture tests impiden que el core dependa de resolvers concretos" is asking for a **second instance of the same narrow pattern** (ban concrete resolver imports outside a seam), not a general hexagonal-boundary test that doesn't exist yet.

3. **`context.Context` already survives the full command path today, in-process — but the actor system is cluster-aware, and Go's `context.Context` cannot cross a wire.** `Engine.SendCommand` passes the caller's `ctx` into GoAkt's `noSender.SendSync(ctx, entityID, cmd, timeout)` (`engine.go:757`), and inside the actor, `goCtx := ctx.Context()` (`event_sourced_actor.go:506`) recovers **the same context value** GoAkt was handed, which is what reaches `behavior.HandleCommand(goCtx, ...)` (`event_sourced_actor.go:527`). So attaching a `TenantID` via `context.WithValue` before `SendCommand` would reach `HandleCommand` today, for a **locally-hosted** entity. But this same file documents (`engine.go:319-337`, `option.go:308-337`) that `SpawnOn` "may place the actor on a remote node" and that behaviors must be explicitly pre-registered so they can be **deserialized** on the receiving node — a strong signal (though `UNVERIFIED` at the GoAkt wire-protocol level, since GoAkt v4.5.4 is not in the local module cache) that only what travels in the **proto envelope** survives a cross-node hop; arbitrary `context.Context` values almost certainly do not. This is the single most important constraint TENANT-001 hands to TENANT-002 (tenant-aware envelopes): **`context.Context` propagation is a same-process convenience, not a cluster-wide propagation mechanism.**

4. **The framework already has the exact extension-point pattern a `TenantResolver` SPI would use, proven four times over.** `Encryptor`, `Telemetry`, event adapters, and snapshot/offset stores are all optional capabilities wired through `Config` → `GoaktOptions()` conditionally (`option.go:141-156`), registered as a GoAkt `extension.Extension` (`internal/extensions/extensions.go`), and looked up per-actor at `PreStart` via `ctx.Extension(ID)` (`event_sourced_actor.go:219-236`). A `TenantResolver` extension is not a new pattern to invent; it is the fifth instance of one that already exists.

---

## 1. Scope and provenance of the request

- Issue `#45` is filed; its listed children `TENANT-002`..`TENANT-008` are **not yet filed as issues** — only the epic (`#23`) previews their scope. The epic's scope list is treated as authoritative for "what #001 must not foreclose," not as scope for this change.
- `#45` depends on `#10`, which is itself an epic ("Hexagonalize the framework"), not a single completed piece of work. §2 below establishes what actually exists from `#10` today versus what `#45`'s acceptance criteria assume exists.
- Nothing in this repo indicates `#10` is closed or superseded; there is no `openspec/changes/*hexagonal*` or `*arch*` directory. `UNVERIFIED`: whether `#10` has separate deliverables already merged outside what `logger_architecture_test.go` covers — no such evidence was found in this checkout.

---

## 2. What "#10 hexagonalization" concretely is today, and how its one enforcement mechanism works

### 2.1 The mechanism, read directly

`logger_architecture_test.go:104-160` implements `TestKitLoggerIsTheOnlyLoggingBackend`:

- It walks the repo root with `filepath.WalkDir` (`logger_architecture_test.go:114`), skipping a fixed directory allowlist (`vendor`, `example`, `benchmark`, `.git`, `.idea`, `.codegraph`, `.atl`, `openspec` — `logger_architecture_test.go:77-86`).
- It filters to first-party production Go source: not `_test.go`, not `.pb.go` (`logger_architecture_test.go:91-102`, `isScannedGoSource`).
- For **every** scanned file it does a raw `strings.Contains` check against `bannedLoggerConstructs` (`logger_architecture_test.go:42-47`): literal substrings like `"log.NewZap("`, `"go.uber.org/zap"`, `"log.DefaultLogger"`, `"log.DiscardLogger"`.
- For every scanned file **except** the one designated seam file (`logger.go`, `logger_architecture_test.go:37,147-149`), it additionally checks `bannedOutsideLoggerSeam` (`logger_architecture_test.go:55-70`): the literal import string `"github.com/tochemey/goakt/v4/log"`, the literal `"\t\"log\"\n"` (stdlib `log` import with a specific tab/quote/newline shape), and calls like `slog.New(`, `fmt.Printf(`, `log.Fatal`, etc.
- Failure reports the offending file and construct (`logger_architecture_test.go:143-144,151-153`).
- A sanity guard (`logger_architecture_test.go:159`, `require.NotZero(t, scanned, ...)`) fails the test if the walk found zero files, so a broken glob can't silently pass.

**This is not an AST walk and not a `go list -deps`/import-graph check.** It is line-oriented substring matching over raw file bytes, deliberately (the comment at `logger_architecture_test.go:53` says "The scan is a substring match, so every entry is written the way it appears in source"). It has real failure modes a design for #45 must account for: a banned import aliased or wrapped in a build-tag-guarded file would still be caught (substring match doesn't care about syntax), but a banned construct split across a line continuation, or referenced only via a local alias without the literal banned string appearing, would slip through. It also cannot express "no package in this subtree may transitively import package X" — it only sees literal strings in files it visits itself, so an indirect leak through a helper package (`ego` doesn't itself import HTTP, but a package it imports does) would not be caught unless that intermediate package is also scanned and also contains the literal banned string.

### 2.2 Is the rest of the core hexagonal today? No — and that's fine, because the test doesn't claim it is.

The core (`engine.go`, `option.go`, `behavior.go`, `saga.go`, `persistence/*.go`) freely imports:
- `github.com/tochemey/goakt/v4/actor`, `.../extension`, `.../supervisor` (`option.go:27-29`)
- `go.opentelemetry.io/otel/*` (`event_sourced_actor.go` imports, `telemetry.go`)
- `google.golang.org/protobuf/proto` (`behavior.go:29`, `saga.go:30`)

None of this is banned by the existing test — it only bans a specific axis (concrete logging backends outside one seam file). There is **no existing test asserting "core imports zero transport/HTTP/JWT types"** because nothing in core imports any transport/HTTP/JWT types today (§10 below) — so there has been no need to write one. `#45`'s acceptance criterion effectively asks to author that guard proactively, for a dependency category (tenant resolvers) that doesn't exist yet, following the one template this repo has for "ban concrete backend X outside seam file Y."

### 2.3 What this means for #45's "architecture tests" criterion

A `TestTenantResolverIsNotConcrete`-shaped test (mirroring §2.1) is straightforward to write with the exact same technique: ban `net/http`, `github.com/golang-jwt/*`, `github.com/ory/*`, and any concrete resolver package path, outside one seam file (or outside a designated adapter package, e.g. `tenancy/` implementations, analogous to how `logger.go` is the one exempted GoAkt-aware file). This is evidence-backed, low-effort, and consistent with repo convention — flagged in §13 as a "safe to propose" finding, not a design decision needing human input.

What is **not** free: a substring scan cannot verify "the core never calls a concrete resolver's `Resolve` method" if that call is mediated through an interface value with no literal banned string in the calling file — which is actually the *desired* end state (core calls the `TenantResolver` interface, never a concrete implementation). The test can really only guarantee "core files contain no literal reference to a concrete resolver package," which is a real but partial guarantee, same caveat that already applies to the logging test today.

---

## 3. Confirming greenfield status (tenant + adjacent concepts)

- `rg -i tenant` across the whole tree: **zero matches**, confirming the task's own premise.
- A broader scan for `org|orgID|account|workspace|namespace|realm` (word-boundary, both cases) returns 53 files, but **every single match is a false positive**: `google.golang.org` import paths (`org` inside the domain), `go.uber.org/atomic` similarly, and `testpb.Account` — a **test fixture bank-account type** used throughout `event_sourced_actor_test.go` / `durable_state_actor_test.go` as the example aggregate (e.g. `event_sourced_actor_test.go:850` `&testpb.Account{AccountId: persistenceID, AccountBalance: 100}`), which has nothing to do with tenancy — it's the domain example, analogous to "BankAccount" in most ES tutorials.
- No `Namespace`, `Realm`, `Org`, or `Workspace` type, field, or config knob exists anywhere in first-party code, protos, or the persistence interfaces (confirmed by direct reads of `egopb/ego.pb.go`, `persistence/events_store.go`, `persistence/state_store.go`, `offsetstore/offset_store.go` — none carry any field beyond `persistence_id`, `sequence_number`, `shard`, `timestamp`, `projection_name`).

**Conclusion: nothing today partially occupies the tenancy space.** TENANT-001 is starting from an entirely clean slate for this concept, which both simplifies the design space (no existing convention to reconcile) and removes any excuse to skip explicit modeling (no accidental scoping to lean on either).

---

## 4. `TenantID` shape space

### 4.1 The dominant idiom: every identifier in this codebase is a bare `string`

| Identifier | Declaration | Shape |
|---|---|---|
| `persistence_id` (proto) | `protos/ego/ego.proto:12` | `string persistence_id = 1;` |
| `entityID` | `engine.go:630,724` | plain `string` parameter |
| `persistenceID` (erase) | `engine.go:945` | plain `string` parameter |
| `sagaID` | `engine.go:906`, `saga.go:79` | plain `string` |
| `projection_name` (proto) | `protos/ego/ego.proto:76,86` | `string` |
| `EventPublisher.ID()` / `StatePublisher.ID()` | `publisher.go:44,79` | returns `string` |
| encryption `keyID` | `encryption/encryptor.go:32-35` | plain `string` parameter |

There is **no** precedent anywhere in this repo for a typed wrapper ID (`type XID string` or a struct). Grepping for `type \w+ID\b (string|struct)` across the module finds only `egopb.ProjectionId` (a generated protobuf **message**, not a scalar wrapper — it bundles `projection_name` + `shard_number`, `egopb/ego.pb.go:493`). That is the one place the codebase reaches for a compound identifier, and it does so as a protobuf message, not a Go-side value type.

### 4.2 Design-space options for `TenantID`, with tradeoffs

| Option | Pros | Cons | Precedent fit |
|---|---|---|---|
| `type TenantID = string` (alias) | Zero friction with existing string-keyed APIs (map keys, log fields, proto `string` fields); trivially JSON/proto-serializable; matches every other ID in the framework | No compile-time distinction from a raw string — a caller can pass any string as a `TenantID` with no normalization guarantee; zero-value (`""`) is silently a valid-looking value, which conflicts with the "no implicit defaults" principle (`#45` body) | Strongest fit to existing idiom (§4.1) |
| `type TenantID string` (defined type, not alias) | Same serialization ergonomics (a `string`-kinded type marshals as a JSON string and as a proto `string` field with zero glue), but the Go type system now stops accidental mixing with unrelated `string` params (e.g. `entityID`) at compile time; can carry methods (`.Valid()`, `.String()`) and a documented `IsAdministrative()`-style helper without behaving like a struct | Slightly more friction than a raw string at API boundaries (explicit conversions needed); does not by itself solve normalization or the empty-string-is-still-a-valid-TenantID problem — needs a constructor (`NewTenantID(s string) (TenantID, error)`) to actually enforce non-empty/normalized values, otherwise the type-safety win is cosmetic | Deviates from repo idiom, but only by degree — `EntityKind` (`option.go:317`, `type EntityKind = extension.Dependency`) shows the codebase *does* use defined/aliased types for domain concepts already, just not for scalar IDs |
| Struct (`type TenantID struct { value string }`) with a constructor and validated invariants | Strongest guarantee against zero-value misuse (a struct can refuse to expose its zero value as "valid"); can bundle normalization state (already-lowercased flag, etc.) | Breaks map-key/comparability ergonomics only if the struct isn't itself comparable (a struct with only a string field *is* comparable and usable as a map key, so this con is smaller than it looks) — real cons are: heavier for GoAkt actor-name/partition-key use (§6.3 in the resolver section) since that API wants a plain string, and it is a genuine break from every other ID in the framework, which invites the question "why is tenant special and no other ID is" | No precedent anywhere in this codebase — would be the first |

**Zero-value safety** is the crux: in "no defaults in multi-tenant mode" mode, the empty string (or the zero value of any wrapper) must be treated as "absent," and the framework must actively reject an absent tenant before persistence access (`#45`'s explicit acceptance criterion). That rejection has to happen through a **helper function** (`RequireTenant(ctx) (TenantID, error)` or similar), not through the type system alone — Go cannot make a type genuinely un-zero-value-constructible while remaining a comparable, map-key-friendly primitive. This applies equally whether `TenantID` ends up a string alias or a defined string type; a struct buys slightly more zero-value safety at the cost of ergonomics and precedent-fit.

**Serialization:** `egopb/ego.pb.go` shows this project generates protobuf Go types with plain `string` fields (`PersistenceId string`, `egopb/ego.pb.go:29`) — a defined Go string type (`type TenantID string`) round-trips through both `encoding/json` and generated proto-Go structs with zero glue code as long as the *proto* field itself stays `string tenant_id = N;` (protoc-generated Go for a `string` field is always a plain `string`, so the framework-side `TenantID` would need an explicit conversion at the proto boundary regardless of which Go shape is chosen — this cost is identical across all three options).

**UUID vs. opaque string vs. both:** nothing in the codebase mandates UUIDs for any existing ID (`persistenceID` is caller-supplied, arbitrary). The natural default is "opaque string, framework does not mandate a format," leaving format validation (UUID-only, slug-only, etc.) as an application/adapter-level concern layered on top of `TenantID`'s constructor, not baked into the core type.

**Normalization (case, whitespace, charset):** not addressed anywhere in the codebase today for any identifier — `persistenceID`, `entityID`, `keyID` are all used verbatim, case-sensitively, with no trimming. A `TenantID` normalization rule (e.g., case-fold + trim) would be a **new** policy for the framework, not an extension of an existing one. This is a genuine open decision (§13b), not something the codebase already answers.

---

## 5. `TenantContext` propagation — traced through the real command/event/query path

### 5.1 The path, end to end, with line evidence

```
caller's ctx (e.g. HTTP handler)
        │
        ▼
Engine.SendCommand(ctx, entityID, cmd, timeout)      engine.go:724
        │  optional OTel span wraps ctx here          engine.go:736-750
        ▼
ref.noSender.SendSync(ctx, entityID, cmd, timeout)   engine.go:757   (*goakt.PID, GoAkt v4)
        │  ── in-process: GoAkt hands the same ctx to the actor's ReceiveContext
        │  ── UNVERIFIED cross-node: GoAkt v4.5.4 source not in local module cache;
        │     ego's own docs (below) strongly suggest ctx values do not survive a
        │     remote hop today
        ▼
EventSourcedActor.Receive → processCommandAndReply(ctx *goakt.ReceiveContext, ...)  event_sourced_actor.go:505
        │
        ▼
goCtx := ctx.Context()                                event_sourced_actor.go:506   ← recovers the original context.Context
        │
        ▼
entity.behavior.HandleCommand(goCtx, command, ...)    event_sourced_actor.go:527   ← this IS the domain boundary (behavior.go:61)
```

The same `ctx.Context()` pattern reaches `HandleEvent` during recovery/replay (`event_sourced_actor.go:327,392,410`, all typed `ctx context.Context`), and `buildEnvelopes`/`marshalEvent` (`event_sourced_actor.go:560,593`) which is where events get turned into `*egopb.Event` for persistence — **this is the exact point where a tenant-aware envelope (TENANT-002) would need to stamp a `tenant_id` field onto the persisted record**, because it is the last point in this call chain where both the resolved tenant (from `ctx`) and the about-to-be-persisted proto message are simultaneously in scope.

The same shape repeats for sagas: `saga_actor.go:337,391` calls `noSender.SendSync(ctx, cmd.EntityID, cmd.Command, timeout)` — a saga forwarding a command to another entity goes through the identical `SendSync` seam, so any tenant-attachment/verification helper placed at that seam covers both direct engine calls and saga-issued inter-entity commands with one implementation.

### 5.2 The cross-node caveat (critical, and load-bearing for TENANT-002+)

`option.go:319-337` (`WithEntityKinds` doc comment) and `engine.go:584-590` are explicit: a spawn "may be placed on a remote node," and "the receiving node reconstructs the behavior against its own type registry" — behaviors travel as **serialized dependencies** through GoAkt's own dependency-registry mechanism, not as live Go values. `internal/extensions/extensions.go:341-348,369-376` shows the concrete mechanism for *config* dependencies: explicit `MarshalBinary`/`UnmarshalBinary` (JSON) implementing `extension.Dependency`. **Nothing in this codebase carries an arbitrary `context.Context` across that serialization boundary** — `context.Context` is famously non-serializable in Go generally, and there is no code here that even attempts it.

Consequence: attaching `TenantID` purely via `context.WithValue` gets you tenant propagation **within a single node's call stack** (Engine entry point → actor → behavior, all in-process) but is a hard **dead end** the moment GoAkt clusters and a spawn or a command lands on a different physical node. This is `UNVERIFIED` at the GoAkt wire-protocol level (v4.5.4 not available locally to inspect `SendSync`'s marshaling path directly), but it is the only conclusion consistent with everything this repo's own comments say about how cluster placement works. **This is the single most consequential finding for scoping #001 correctly**: the `TenantContext`/context-attachment helpers this issue asks for are necessary but not sufficient for cluster correctness — TENANT-002 (tenant-aware envelopes) is not an optional nice-to-have layered later, it is the mechanism that makes tenant identity survive the exact boundary GoAkt already crosses for entity relocation. #001 should document this explicitly as a stated constraint on its own deliverable, without attempting to solve it.

### 5.3 Every point a tenant would need to be attached or read

| Point | Evidence | Attach or read? |
|---|---|---|
| `Engine.SendCommand` entry | `engine.go:724` | Read (must already be on `ctx`, or reject) |
| `Engine.Entity` / `DurableStateEntity` (spawn) | `engine.go:573,667` | Read/attach — establishes tenant scope for an entity's lifetime? Or per-command? (open decision, §13b) |
| `Engine.Saga` | `engine.go:863` | Read |
| `saga_actor.go` inter-entity `SendSync` | `saga_actor.go:337,391` | Read (forwarded, not re-resolved) |
| `EventSourcedActor.processCommandAndReply` → `HandleCommand`/`HandleEvent` | `event_sourced_actor.go:506,527` | Read only — `behavior.go` must stay resolver-agnostic (§10) |
| `buildEnvelopes`/`marshalEvent` before persistence | `event_sourced_actor.go:560,593` | Read, to stamp envelope (future TENANT-002 concern, not this issue) |
| `Engine.StartProjection`/`RebuildProjection` | `engine.go:376,496` | Read/scope (future TENANT-004 concern) |
| `Engine.EraseEntity` (administrative op) | `engine.go:945` | Currently **zero** tenant awareness — concrete existing example of the epic's "administrative operation" case (§8) |
| `testkit` scenario `When()` | `testkit/scenario.go:112` | Hardcodes `context.Background()` today — **no seam to inject a tenant-bearing context in tests at all** (§6.5) |

---

## 6. `TenantResolver` SPI — shape space, not a signature

### 6.1 Input neutrality

The framework's existing SPI-shaped interfaces (closest analog: `encryption.Encryptor`, `encryption/encryptor.go:29-36`) take `context.Context` as their first parameter and otherwise only framework-native types (`persistenceID string`, `[]byte`). A `TenantResolver` following that exact convention — `Resolve(ctx context.Context) (TenantID, error)` — is transport-neutral by construction: it never sees an `*http.Request`, a JWT claims struct, or an Ory session. The epic's principle "el tenant nunca se obtiene directamente desde HTTP/JWT dentro del core" is satisfied by this shape alone, *provided* the actual extraction from HTTP/JWT happens in an adapter that populates `ctx` (via `context.WithValue` or a documented attach-helper) **before** calling anything in core — i.e., the resolver's job in-core is to read what an out-of-core adapter already put on the context, or to apply a fallback policy (e.g., single-tenant mode) when nothing was attached. This mirrors exactly how `kitlog.Logger` is handed to the framework fully-formed (`option.go:206-210`) rather than the framework reaching into a concrete logging backend.

### 6.2 Sync-only vs. async/error-returning

Every SPI-like interface in this repo returning fallible results does so via `(T, error)`, synchronously, with the caller responsible for whatever timeout the passed `ctx` carries (`persistence.EventsStore`, `encryption.Encryptor`, `offsetstore.OffsetStore` — all `(ctx, ...) (T, error)`, never a channel or callback shape). A `TenantResolver.Resolve(ctx) (TenantID, error)` matches that idiom directly; nothing in the codebase uses an async/future-returning SPI shape anywhere, so introducing one here would be a first, not an extension of a pattern.

### 6.3 Typing the three (or four) failure modes

The issue names three: missing, invalid, forbidden — the epic's acceptance criteria implies a fourth is worth considering: *unknown* (tenant well-formed but not recognized by the resolver's backing store) and *ambiguous* (multiple resolvers disagree, §6.4). The codebase's two error idioms:

- **Sentinel errors via `errors.New`**, checked with `errors.Is` — the dominant style (`engine.go:57-80`: `ErrEngineNotStarted`, `ErrUndefinedEntityID`, etc., 8 sentinels in that one file). Good for cheap, information-free failures.
- **Typed error structs implementing `Error()`/`Unwrap()`**, used when the failure needs to carry structured context — `projectionRunnerError` (`projection_runner.go:827-839`, wraps an inner error) and `handlerPanicError` (`projection_runner.go:842-858`, carries `value any` and `stack []byte`).

For a tenant error model, sentinel-only would lose exactly the information an HTTP/gRPC adapter needs to map to a status code (which tenant was rejected, and why) — the codebase's own `handlerPanicError` precedent argues for **a small typed error** (e.g. an unexported struct implementing `error` with an exported reason enum plus `Unwrap() error`, and package-level sentinel values for `errors.Is` compatibility on the reason itself — i.e. both idioms combined, which is what `projectionRunnerError` already effectively does by wrapping). This lets `errors.Is(err, tenancy.ErrTenantMissing)` work for simple branching while still exposing the offending `TenantID` string via a typed accessor for logging/mapping. This is presented as a shape, not a final design — the actual reason taxonomy (missing/invalid/unknown/forbidden/ambiguous) is an open decision (§13b).

### 6.4 Composition (chain/priority/fallback) and the single-tenant resolver as a built-in fallback

Nothing in the codebase composes SPI implementations today — `Encryptor`, `EventsStore`, etc. are each singular, one-per-`Config` (`option.go:46-56`). A composing `TenantResolver` (chain-of-resolvers, first-match-wins, or explicit priority) would be a **new compositional pattern** for this framework, not an extension of one. The natural anchor point: `ResolveLogger`'s nil/typed-nil fallback pattern (`logger.go:80-89`, `ResolveLogger`) is the closest precedent for "if X is nil or unusable, fall back to a documented default" — a single-tenant-mode resolver slotting in as `Config`'s default `TenantResolver` when none is configured (mirroring how `DefaultLogger()` is `Config`'s default when no `WithLogger` was given) is a very close structural fit and should be the anchor for how `single_tenant_mode` and "a resolver was never configured" reconcile (§7).

### 6.5 Testing/fake story

The repo's existing seam-faking pattern is `mockery`-generated mocks under `mocks/<package>/<Type>.go` (`mocks/encryption/encryptor.go:1` — "Code generated by mockery. DO NOT EDIT.", with a fluent `EXPECT()` builder). A `TenantResolver` SPI would get the same treatment (a generated mock under, e.g., `mocks/tenancy/resolver.go`), plus — critically — `testkit/scenario.go` currently has **no way to inject a tenant-bearing (or any custom) `context.Context`** into a scenario: `When()` hardcodes `context.Background()` (`testkit/scenario.go:112`). Any tenant-aware behavior test would need a new `WithContext(ctx)` builder method on `EventSourcedScenario`/`DurableStateScenario` before it could exercise tenant-dependent logic at all — this is a concrete, small, and currently-missing piece of infrastructure this issue's scope should flag even though it belongs to implementation, not exploration.

---

## 7. `single_tenant_mode` and the existing `Config`/`Option` pattern

`Config` (`option.go:46-64`) is a plain struct populated exclusively through `NewConfig(eventsStore, opts...)` + `Option` functional values (`option.go:180-197`, the `OptionFunc` adapter). Every existing capability toggle follows the same shape: `WithX(value) Option` that does `c.x = value` (`WithLogger`, `WithStateStore`, `WithTelemetry`, `WithEncryptor` — all one-liners, `option.go:206-355`). There is **no existing boolean-mode toggle** in `option.go` today (no `WithClusterMode(bool)` or similar) to use as a direct precedent for a mode switch specifically, but the pattern trivially generalizes: `WithSingleTenantMode(id TenantID) Option` (or `WithTenantResolver(r TenantResolver) Option`, mutually exclusive with the former) sets fields on `Config` exactly like every other option, and `NewConfig`'s post-loop resolution step (`option.go:85-88`, which already does exactly this for the logger: "Options may have set a nil or typed-nil logger... Resolving after the loop covers every option path") is the natural place to apply "if neither a resolver nor a fixed single-tenant ID was configured, default to X" — where X (fail hard vs. silently default to multi-tenant-with-no-default) is precisely the "no implicit defaults in multi-tenant mode" principle the epic states, and needs to be an explicit human decision (§13b), not inferred from the `WithLogger` precedent (logging *does* have a safe silent default; tenancy explicitly must not, per the epic's own stated principle).

The requirement "sin plumbing adicional en la aplicación" for single-tenant deployments maps cleanly onto this: a `WithSingleTenantMode(fixedID)` option installs a resolver-shaped value that always returns `fixedID` regardless of `ctx`, so callers never write `ego.WithTenant(ctx, id)` at every call site — they configure it once at `NewConfig` time. This is fully consistent with the existing `Option` pattern and needs no new mechanism, only a new `Option`.

---

## 8. Administrative/system context vs. a real tenant

The codebase already contains one concrete example of the "cross-tenant administrative operation" the epic calls out: **`Engine.EraseEntity(ctx, persistenceID, full)` (`engine.go:945-974`)** — it deletes events/snapshots for a `persistenceID` directly against `eventsStore`/`snapshotStore`, with **zero** tenant scoping of any kind today (there's nothing to scope against yet). This is a real, load-bearing precedent for what "administrative context" needs to mean in practice: an operator calling `EraseEntity` today crosses whatever tenant boundary might exist without even being aware one exists, because none does yet. Any tenant design that doesn't explicitly account for this call path leaves it as a silent bypass by omission, which is exactly the failure mode the epic principle "un administrative/system context no equivale a tenant vacío" is trying to prevent.

Options, compared against that concrete example and the framework's own idioms:

| Approach | Pros | Cons |
|---|---|---|
| Sentinel `TenantID` value (e.g. `"system"` / `TenantSystem TenantID = "__system__"`) | Trivial to implement; reuses the same type everywhere (no new type in call signatures); trivially logged/serialized | Exactly the anti-pattern the epic principle warns against — a sentinel string is indistinguishable from a real (if oddly named) tenant unless every consumer special-cases it; a resolver bug or a customer literally naming a tenant `"system"` (nothing currently validates against it, §4.2) creates ambiguity; not type-safe |
| `nil`/empty tenant means admin | Cheapest to implement | Directly forbidden by the epic ("un administrative/system context no equivale a tenant vacío") and by the "no implicit defaults" principle — empty must mean "missing/reject," not "admin." Not viable. |
| Dedicated `AdministrativeContext` / scope type, distinct from `TenantID`, attached to `ctx` via its own helper | Type-safe distinction — code that checks "is this a real tenant" and code that checks "is this administrative" cannot be confused by construction; naturally auditable (a distinct field name shows up distinctly in any structured log built from context, e.g. `admin_actor`, `admin_reason` vs `tenant_id`) — matches the epic's explicit "auditable" requirement | New concept to design (who is "the administrator" — a string identity? a reason code? both?); needs its own attach/read helpers alongside `TenantID`'s, roughly doubling the helper surface this issue already promises ("helpers seguros para attach/read tenant") |

Given the framework's existing typed-error precedent (§6.3) and the epic's explicit "auditable" requirement, **a dedicated type is the only option that satisfies the epic's own stated principle** — the sentinel and nil options are both explicitly ruled out by the epic text itself, not just by general good practice. What that type actually carries (actor identity? justification string? both?) is an open decision (§13b) that this exploration deliberately does not resolve.

---

## 9. Error model — grounded in the codebase's two existing idioms

See §6.3 for the mechanism (sentinel + typed-wrapper, combined, matching `projectionRunnerError`). Concretely for the tenant domain, the minimal reason taxonomy implied by the issue text is:

- **Missing** — no tenant resolvable and not in single-tenant mode (must fail *before* persistence access, per acceptance criteria — meaning this check needs to happen at the `Engine.SendCommand`/`Entity`/`Saga` entry points in `engine.go`, not inside `persistence.EventsStore` implementations, which have no concept of tenancy today and shouldn't be made to grow one just to enforce this).
- **Invalid** — a `TenantID` failed whatever normalization/charset rule §4.2 leaves open.
- **Unknown/Forbidden** — resolver-specific; not something core can distinguish without delegating to the resolver's own error, which argues for the resolver's `error` return being wrapped (`Unwrap()`) rather than core re-deriving a reason from a raw resolver failure.

Each reason needs to be **distinguishable by an adapter without depending on error string matching** — i.e., `errors.Is`/`errors.As` friendly, exactly the `ErrEngineNotStarted`-style sentinel pattern already used pervasively (`engine.go:57-80`) for the coarse case, with a typed wrapper (`projectionRunnerError`-style) for anything that needs to carry the offending `TenantID` or the resolver's underlying error for logging. This is presented as a pattern to follow, not a finalized set of exported names.

---

## 10. What must NOT depend on concrete tenant resolution — verified file by file

| File/package | Current import surface (relevant excerpt) | Verdict |
|---|---|---|
| `behavior.go` | `context`, `goakt/v4/extension`, `google.golang.org/protobuf/proto` (`behavior.go:26-30`) | Clean — no logging, no transport, no tenancy. Must stay this way; `HandleCommand`/`HandleEvent` should only ever see a `TenantID` if the caller chooses to pass tenant data as part of `Command`/`State` payload — the interface signature itself (`behavior.go:61,64`) must not change to add a tenant parameter, or every existing implementation breaks. |
| `saga.go` | `context`, `time`, `goakt/v4/extension`, `proto` (`saga.go:26-30`) | Clean, same constraint as above. |
| `testkit/scenario.go` | `context`, `fmt`, `testing`, `testify`, `proto` (`testkit/scenario.go:25-33`) | Confirmed logger-free (matches the prior kit-logger exploration's finding) and confirmed **tenant-free and infra-free** today; `When()` hardcodes `context.Background()` (`scenario.go:112`), which is a gap, not a violation — needs a `WithContext` builder addition to be tenant-testable, but currently imports nothing that would need to change. |
| GoAkt actor layer (`event_sourced_actor.go`, `durable_state_actor.go`, `saga_actor.go`) | `goakt/v4/actor` types, OTel, `egopb` | Already imports GoAkt concretely (it *is* the actor layer) — a `TenantResolver` extension lookup here (`ctx.Extension(ID)`, exactly like `loadOptionalExtensions`, `event_sourced_actor.go:219-236`) would be consistent with existing coupling, not a new violation. This is the natural plug-in point if the engine itself is to *invoke* a resolver rather than merely *read* an already-resolved `ctx` value. |
| `publisher/kafka`, `publisher/nats`, `publisher/pulsar`, `publisher/websocket` | `kafka.Config` imports `IBM/sarama`, `crypto/tls`, `kitlog` (`publisher/kafka/config.go:25-31`); `websocket.Config` imports `tochemey/gopack/validation` (grep) | None import HTTP/JWT/Ory. Topics are single flat strings (`EventsTopic`, `StateTopic`, `kafka/config.go:51-54`) with **zero** tenant-scoping mechanism today — confirms TENANT-005 (topic isolation) is fully greenfield; #001 doesn't need to touch these files, but should not assume topic-per-tenant is solved by anything that exists. |
| `migration/migration.go` | `kitlog`, `proto`, `anypb`, `ego`, `egopb`, `persistence` (`migration.go:47-58`) — no transport imports | Iterates **all** persistence IDs unconditionally (structurally confirmed — no tenant filter exists because there's nothing to filter on) — another concrete greenfield confirmation for cross-tenant admin operations (alongside `EraseEntity`, §8). |
| `option.go` / `engine.go` | GoAkt, OTel, `internal/extensions`, `persistence`, `projection` — no transport/HTTP/JWT | These are exactly where a `WithTenantResolver`/`WithSingleTenantMode` `Option` and a `Config.tenantResolver` field would live, following the identical pattern already used for `logger`, `encryptor`, `telemetry` (`option.go:46-64,199-355`). |

**Proposed plug-in point (design-space, not a commitment):** the `internal/extensions` + `ctx.Extension(ID)` mechanism (§0.4, `internal/extensions/extensions.go`, `event_sourced_actor.go:219-236`) is the mechanism already used for every other optional per-actor capability, and is the most consistent place for a `TenantResolver` (if the engine itself invokes it, as opposed to only reading an already-resolved value off `ctx`) to be delivered to actors without any core file needing to import a concrete resolver package — `Config.GoaktOptions()` would conditionally add `extensions.NewTenantResolver(c.tenantResolver)` exactly like `if c.encryptor != nil { ... }` does today (`option.go:154-156`).

---

## 11. Architectural enforcement — extending the existing mechanism

Given §2's finding that the only enforcement mechanism in this repo is a substring-scanning `filepath.WalkDir` test with one designated seam file, a `#45`-satisfying test is a direct structural copy of `logger_architecture_test.go`:

- Ban literal strings: `"net/http"`, known transport/auth library import paths (`golang-jwt`, `ory` client packages, `google.golang.org/grpc` if/when adopted, Kafka/NATS/Pulsar client packages), and the import path(s) of whatever concrete `TenantResolver` implementations ship (e.g. a hypothetical `tenancy/jwtresolver` package) — everywhere **except** a designated adapter/seam location.
- Reuse the same skip-dir list (`example`, `benchmark`, `vendor`, tooling dirs) and the same `_test.go`/`.pb.go` exclusion.
- The same caveats apply: it's a byte-level guard, not a real import-graph analysis, and it can't detect indirect leakage through an intermediate first-party package that itself isn't scanned for the same strings (it would be, since the walk covers the whole tree — but a *third-party* package the core imports that itself imports `net/http` transitively would not be caught, since the scan only reads first-party `.go` files, not `go list -deps` output). A more rigorous version could shell out to `go list -deps ./...` and check the transitive closure for banned import paths — this is a stronger, previously-unused technique in this repo, and worth flagging as an option (not a decision) since it would catch transitive leaks the substring scan cannot.

---

## 12. How #001's choices constrain #002–#008

| #001 choice | Blocks / unblocks |
|---|---|
| `TenantID` as string-alias vs. defined type vs. struct (§4.2) | **TENANT-002** (envelope fields): a struct `TenantID` cannot be a proto scalar field directly and would need explicit conversion at every marshal/unmarshal site; a string-kinded type is friction-free there. **TENANT-003** (store isolation): if `TenantID` isn't comparable/hashable as cleanly as a string, every store's tenant-scoped query/index design gets more complex for no benefit. |
| Whether `TenantContext` propagation is documented as same-node-only (§5.2) | **TENANT-002** must exist and must be the actual propagation mechanism for anything cluster-relevant — if #001 ships helpers that *imply* `context.Context` is sufficient end-to-end, TENANT-002 inherits a false assumption that will surface as a correctness bug under clustering, not a design review comment. |
| Resolver invoked by the engine (extension-based, §10) vs. purely a caller-side contract with no engine invocation | **TENANT-006** (single-tenant mode): if the engine never calls the resolver itself, "no plumbing" for single-tenant apps is harder to deliver (some caller-side wrapper is still needed at every entry point). If the engine does call it, extension-registration precedent (§10, actor layer) means every node needs the same resolver wiring, exactly like every other extension today — a real operational constraint for TENANT-006 and TENANT-007. |
| Error model granularity (§9) | **TENANT-007** (conformance tests): cross-tenant test assertions need to distinguish "no tenant" from "wrong tenant" from "system op" precisely to assert the right failure mode per scenario; an under-specified error model here means TENANT-007 either invents its own taxonomy (fragmenting the contract) or blocks on #001 revisiting it. |
| Administrative-context shape: sentinel vs. dedicated type (§8) | **TENANT-008** (admin semantics) is *the* consumer of this decision directly — if #001 ships a sentinel, TENANT-008 either lives with the ambiguity the epic explicitly forbids, or has to introduce the dedicated type retroactively as a breaking follow-up. |
| Whether tenant scoping is per-entity (attached at spawn) or per-command (attached at every `SendCommand`) — not resolved in this exploration | **TENANT-002/003**: per-entity scoping implies the tenant is effectively part of entity identity (composes with `persistenceID`, §5.3); per-command scoping implies every single command must carry/resolve a tenant independently, which is more flexible but means a compromised or buggy caller could in principle send a command for tenant A against an entity that "belongs" to tenant B unless TENANT-002/003 separately enforce identity-tenant binding. This is a genuinely open architectural fork #001 should surface explicitly to #002 rather than silently pick. |
| Single-tenant-mode default behavior when neither a resolver nor a fixed ID is configured (§7) | **TENANT-006** inherits whatever "fail hard" vs. "silent default" choice #001 makes here as its literal specification — this is not a detail, it's TENANT-006's entire acceptance criterion in miniature. |

---

## 13. Risks and open decisions

### (a) Findings safe enough to move straight into `sdd-propose`

1. The codebase is genuinely greenfield for tenancy — no adjacent concept exists to reconcile (§3).
2. `TenantResolver.Resolve(ctx context.Context) (TenantID, error)`-shaped input neutrality, synchronous/error-returning, is consistent with every existing SPI-like interface in this repo (`encryption.Encryptor`) — the *shape family* is safe to commit to; the exact method name/signature is not.
3. The extension pattern (`internal/extensions` + `ctx.Extension(ID)` + conditional wiring in `Config.GoaktOptions()`) is the correct, proven mechanism to model a `TenantResolver` plug-in point on, mirroring `Encryptor`/`Telemetry`/event adapters (§10).
4. `WithSingleTenantMode(...)`/`WithTenantResolver(...)` as `Option` functions on `Config`, resolved post-loop in `NewConfig` (mirroring the existing `ResolveLogger` post-loop pattern, `option.go:85-88`), is a direct, low-risk fit to the existing functional-options architecture (§7).
5. A `logger_architecture_test.go`-style substring-scan test banning transport/auth import strings and concrete resolver package paths outside a designated seam is directly actionable and satisfies #45's architecture-test acceptance criterion, with the same known limitations as the existing logging test (§2, §11).
6. The sentinel-`TenantID`-for-admin and nil-means-admin approaches are ruled out by the epic's own stated principles, not just by general design taste (§8) — safe to exclude from the design space outright.
7. `behavior.go`/`saga.go` interface signatures must not gain a tenant parameter — any tenant data reaching `HandleCommand`/`HandleEvent` must travel inside `Command`/`Event`/`State` payloads or be attached to `ctx`, never a new positional parameter (§10).

### (b) Decisions that need explicit human input before any design is written

1. **`TenantID` Go shape** (string alias vs. defined string type vs. struct, §4.2) — this is a real, opinionated fork with no codebase precedent to defer to either way.
2. **Normalization policy** (case-folding, whitespace, allowed charset, UUID-only vs. opaque) — the codebase has zero existing precedent for normalizing *any* identifier, so this is a fresh policy question, not an extension of an existing rule (§4.2).
3. **Does the engine itself invoke `TenantResolver`, or is it purely a contract applications/adapters implement and callers are responsible for resolving before calling into `ego`?** This single choice reshapes §7, §10, and the entire operational story for TENANT-006 (§12).
4. **Tenant scoping granularity: per-entity (bound at spawn/`Entity()` call) vs. per-command (resolved on every `SendCommand`)** — not resolved here; flagged in §12 as a fork with real security implications for TENANT-002/003.
5. **Administrative-context payload**: what exactly does "auditable" require it to carry — an actor identity, a justification/reason string, both, a ticket/correlation ID? The epic requires it be auditable but doesn't specify the shape (§8).
6. **Error-reason taxonomy**: missing / invalid / unknown / forbidden / ambiguous — which of these are truly distinct failure modes core needs to model versus which collapse into "resolver said no" (§6.3, §9).
7. **Resolver composition**: does #001 need to define chaining/fallback semantics now (chain-of-resolvers, priority), or is "exactly one configured resolver, with single-tenant-mode as the built-in default when none is set" (§6.4) sufficient for this issue, deferring composition to a later story if it's ever needed at all?
8. **Whether the "architecture test bans concrete resolver imports" mechanism (§11) should be upgraded from substring-matching to a `go list -deps`-based transitive check**, given the substring approach's known blind spot for indirect/transitive leaks through third-party packages.

### (c) Risks to the existing public API surface any tenant design must respect

1. **`behavior.go` / `saga.go` interfaces are the framework's most-implemented public contract.** Any change to `HandleCommand`/`HandleEvent`/`HandleResult`/`HandleError`/`Compensate` signatures (e.g. to thread a `TenantID` parameter positionally) breaks every existing implementer — the codebase's own convention (context-carried data, never positional identity parameters beyond `entityID`/`command`/`state`) argues strongly against ever doing this, but it is a live risk if a future design doc proposes it for "explicitness." (§10, §12)
2. **`testkit/scenario.go`'s `When()` hardcodes `context.Background()`** (`scenario.go:112`) — any tenant-aware behavior becomes untestable via the existing testkit until a `WithContext` (or equivalent) builder method is added. This is a required, currently-missing piece of public API surface, not an internal detail — its shape needs review since `testkit` is itself a versioned public package. (§6.5)
3. **`Engine.EraseEntity` and `migration.Migrator.Run`** are existing public administrative operations that today have no tenant awareness because none exists — introducing tenant scoping later (TENANT-003/008) will need to either (a) leave these operating cross-tenant by design (matching "administrative bypass, if it exists, is explicit and auditable" from the epic) or (b) add tenant-filtering parameters, which is a breaking signature change to two already-public APIs. #001 should not silently assume either outcome. (§8, §10)
4. **`publisher/kafka.Config`, `.../nats`, `.../pulsar`, `.../websocket` topic fields are flat strings** (`kafka/config.go:51-54`) — any future per-tenant topic strategy (TENANT-005) that changes these fields' shape (e.g. from a static string to a template/function) is a breaking change to already-public configuration structs. Not this issue's problem to solve, but its `TenantContext`/`TenantID` shape choices (§4) directly determine how invasive that future change will be (e.g., a string-kinded `TenantID` interpolates into a topic-name template trivially; a struct would need an explicit `.String()` call at every such site).
5. **`option.go`'s `Config` struct has no versioning/deprecation mechanism observed** (options are additive, never removed in the code visible here) — adding `tenantResolver`/`singleTenantID` fields is additive and low-risk to `Config` itself, but the epic's "no implicit defaults in multi-tenant mode" principle means the *behavior* of `NewConfig` potentially changes (rejecting configs that previously worked, once multi-tenant mode is the implied default) — this is a behavioral compatibility risk, not just an API-surface one, and needs explicit sign-off on migration/rollout story before design.
