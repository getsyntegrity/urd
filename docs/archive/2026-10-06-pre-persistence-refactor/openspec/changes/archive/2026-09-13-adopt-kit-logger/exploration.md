# Exploration — Adopt kit-logger as Ego's official logging solution

Date: 2026-09-09
Change: `adopt-kit-logger`
Phase: `sdd-explore` (no code changed)
Repo: `/home/pablog/workspace/pablogore/ego` — module `github.com/tochemey/ego/v4`, fork owned as `github.com/pablogore/ego`

All claims below are backed by `path:line` evidence. Claims that could not be verified are marked `UNVERIFIED`.

---

## 0. Headline

Two findings reframe the request:

1. **The logging seam already exists and is already public.** This fork shipped a complete `ego.Logger` abstraction, a slog-backed default, a `DiscardLogger`, and a GoAkt adapter in v4.4.3, documented as a breaking change (`CHANGELOG.md:392-397,420,456`). "PR A — logging foundation" as originally scoped is ~90% already implemented.

2. **`kit-logger@v0.1.1` is not currently safe to adopt as a framework-wide backend.** It is a solid application-level logger, but it carries six properties that are acceptable in a service and harmful in a library: a package `init()` that mutates the global Prometheus registry, an unconditional log line written to `os.Stdout` at construction, output hardwired to `os.Stdout`, a permanently no-op `Sync()`, a goroutine leak on `With()` when buffering is enabled, and an always-on `runtime.Caller` loop on every record. It also does **not** implement the two features the request assumed it had: redaction and automatic OpenTelemetry trace correlation.

The good news: the *shape* fit is perfect. `kit-logger.Logger` is a strict superset of `ego.Logger`, so `*logger.SlogLogger` satisfies `ego.Logger` with **zero adapter code**. Adoption is a configuration and hardening problem, not an interface problem.

---

## 1. Current logging architecture

### 1.1 First-party logging surface

The framework has one deliberate logging abstraction, not scattered `log.Printf` calls.

| Element | Location | Shape |
|---|---|---|
| `Logger` interface | `logger.go:54-59` | `Debug/Info/Warn/Error(msg string, args ...any)` — slog convention |
| `LeveledLogger` (optional) | `logger.go:69-71` | `Level() string`, used for engine-side gating |
| `DiscardLogger` (noop) | `logger.go:81-83` | `var DiscardLogger Logger = discardLogger{}` |
| `defaultLogger` | `logger.go:~86-93` | delegates to `log/slog` package functions |
| `loggerAdapter` | `logger.go:97-279` | wraps `ego.Logger`, satisfies GoAkt's `log.Logger` (compile-time check `logger.go:104`) |
| `StdLogger()` bridge | `logger.go:268-279` | wraps `Logger.Info` in an `io.Writer` |
| `Config.logger` + default | `option.go` (`NewConfig`) | defaults to `defaultLogger{}` |
| `WithLogger` option | `option.go:205` | `func WithLogger(logger Logger) Option` — **public API today** |
| GoAkt wiring | `option.go:107` | `goakt.WithLogger(newLoggerAdapter(c.logger))` — single wiring point |
| Adapter tests | `logger_test.go` | 529 lines, hand-rolled spy/noop fakes |

Direct log call sites in first-party code: **8**, all in `engine.go`, all publisher-lifecycle and publish success/failure, all built with `fmt.Sprintf`. `engine.go:1190` and `engine.go:1237` are per-publish INFO logs and are the only borderline hot-path sites.

`isNilLogger` (`logger.go:118-125`) already guards typed-nil loggers via reflection — a real robustness detail worth preserving.

### 1.2 Two inconsistencies (both verified)

1. **A second, disconnected logger seam.** `migration/option.go:46` declares `func WithLogger(logger log.Logger) Option` using **GoAkt's** `log.Logger` type, and `migration/migration.go:67` stores it. This package was never migrated to `ego.Logger`. `publisher/kafka/config.go:56` likewise exposes a GoAkt-typed `Logger` field.
2. **A stray zap default inside Ego's own code.** `projection_runner.go:181` hardcodes `logger: log.NewZap(log.ErrorLevel, os.Stderr)` as a struct default. It is dead in production (always overridden at `projection_actor.go:91`) but it is a GoAkt-zap construction living in an Ego file. `projection_runner.go:78` also types the field as GoAkt's `log.Logger`.

### 1.3 Domain boundary — clean

`behavior.go`, `saga.go` and `testkit/scenario.go` are **confirmed logger-free**. `EventSourcedBehavior`, `DurableStateBehavior` and `SagaBehavior` carry no logging dependency in any signature. This constraint is currently satisfied and must be preserved.

### 1.4 OpenTelemetry relationship

`telemetry.go` wires an OTel `Tracer` and `Meter`. There is **no trace↔log correlation today**: `loggerAdapter`'s `*Context` methods receive a `ctx` and **discard it** (`logger.go:215-218, 226-229, 237-240, 248-251`), because `ego.Logger` has no context-aware methods to forward it to. This is the single largest functional gap versus the target.

### 1.5 Dependency provenance

```
$ go mod why -m go.uber.org/zap
github.com/tochemey/ego/v4 -> github.com/tochemey/goakt/v4/log -> go.uber.org/zap

$ go mod why -m github.com/go-logr/logr
github.com/tochemey/ego/v4 -> go.opentelemetry.io/otel -> github.com/go-logr/logr
```

- **First-party zap imports: zero.** `rg -n 'go.uber.org/zap' --glob '*.go' .` → no matches, tests included. The stated goal "zero direct use of zap in our framework" **already holds today**.
- zap enters solely through `github.com/tochemey/goakt/v4/log`, whose package-level vars `DefaultLogger`/`DebugLogger` are `NewZap(...)` (`goakt/v4@v4.5.4 log/zap.go:43,51`). Importing that package compiles zap in. It cannot be dropped while Ego implements GoAkt's `log.Logger` and re-exports it in `migration.WithLogger` and `publisher/kafka.Config.Logger`.
- logr enters through OTel (and GoAkt). Correct and expected; leave it.
- **Vendoring trap:** the repo does **not** carry a `vendor/` tree (`vendor` is in `.gitignore:10`, `git ls-files vendor` empty), yet `Makefile:90-91` runs `go list -mod=vendor` and `go test -mod=vendor`. Running those today fails with `inconsistent vendoring`. Any dependency change therefore requires `go mod tidy` **and** `go mod vendor`.

---

## 2. kit-logger capabilities — measured, not documented

Source: `git clone --depth 1 https://github.com/pablogore/kit-logger`. Public and reachable. HEAD `7125fc8` (2026-08-25); the only tag is **`v0.1.1`**, which is **behind `main`**. Module `github.com/pablogore/kit-logger`, `go 1.26.0` (matches Ego).

Its direct deps: `getsyntegrity/kit-core`, `prometheus/client_golang`, `stretchr/testify`, `golang.org/x/time`, `google.golang.org/grpc`. **No zap, no otel, no logr imported.**

### 2.1 The interface (`pkg/logger/interface.go:9-26`)

```go
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	DebugContext(ctx context.Context, msg string, args ...any)
	InfoContext(ctx context.Context, msg string, args ...any)
	WarnContext(ctx context.Context, msg string, args ...any)
	ErrorContext(ctx context.Context, msg string, args ...any)
	Log(ctx context.Context, level slog.Level, msg string, args ...any)
	With(args ...any) Logger
	WithContext(ctx context.Context) Logger
	SetLevel(level slog.Level)
	Sync() error
	Slog() *slog.Logger
}
```

**This is a strict superset of `ego.Logger` (`logger.go:54-59`).** `*logger.SlogLogger` already satisfies `ego.Logger` with no glue code. That is the decisive compatibility fact.

### 2.2 Reusable — genuinely valuable

| Capability | Evidence | Verdict |
|---|---|---|
| slog-native, exposes `Slog() *slog.Logger` | `slog_logger.go:117` | **Reuse.** Makes every bridge one expression. |
| Accepts an `slog.Handler` (`Config.Handler`) | `config.go:21,72-73` | **Reuse** — but it bypasses the whole decorator chain. |
| Context-aware methods | `interface.go:15-18`, `slog_logger.go:41-63` | **Reuse.** Closes Ego's context gap. |
| `With(args ...any) Logger` child loggers | `slog_logger.go:87-95` | **Reuse** for `component` fields. |
| `GlobalFields map[string]string` | `config.go:84-86` | Reuse for `service`/`node`. |
| Runtime level changes via shared `*slog.LevelVar` | `slog_logger.go:104-108` | Reuse. Note: **no level getter**. |
| Per-key rate limiting `WithRateLimit(key, interval)` | `rate.go:28`, `slog_logger.go:74-80` | **Reuse** for retry/reconnect storms. Emits `suppressed_count`. |
| Handler-level sampling with `MinLevel` bypass | `handler/sampling_handler.go:13-17,43-45` | Reuse cautiously; see 2.3. |
| Hook `func(ctx, slog.Record) (context.Context, bool)` | `config.go:103-105` | **Reuse — this is the OTel correlation hook.** |
| `MultiHandler` | `handler/multi_handler.go:14` | Reuse. |
| Test helpers: `MockLogger`, `TestHandler`, `ExtractAttrs` | `mocklogger.go:18`, `handler/test_handler.go:9,15`, `utils/log_utils.go:15` | Reuse for our integration tests. |

### 2.3 NOT present — the request assumed these exist

| Assumed | Reality |
|---|---|
| **Automatic OTel `trace_id`/`span_id` from context** | **Absent.** `rg -in 'trace_id\|span\|otel\|opentelemetry' --glob '*.go'` → **zero matches**. The only context→fields path is `globalContextFieldExtractor` (`context_extracto.go:8`), consulted **only** in `WithContext(ctx)` (`slog_logger.go:97-102`). The `*Context` methods pass `ctx` straight to slog and never call it. `InfoContext(ctx, ...)` yields **no** trace fields. |
| **Redaction of sensitive fields** | **Absent.** `README.md:8` claims it; there is no redaction code. `FilterHandler` (`handler/filter_handler.go:22-54`) is not redaction — it **drops the entire record** on a case-insensitive key match, with no defaults. |
| Noop/discard logger | **Absent.** No `DiscardLogger`/`NopLogger`. `MockLogger` accumulates every entry in memory unbounded. |
| Level introspection | **Absent.** `SetLevel` is write-only; no getter. |

### 2.4 Defects and library-hostile properties (all verified in source)

| # | Issue | Evidence | Impact on Ego |
|---|---|---|---|
| D1 | `init()` registers `slog_logged_total` into the **default Prometheus registry** and mutates a package var on `AlreadyRegisteredError` | `handler/prometheus_handler.go:22-33` | Importing `pkg/logger` mutates a process-global registry. Ego has **no** Prometheus dependency today. This is a hard, non-optional commitment. |
| D2 | `New()` writes an unconditional `"Logger initialized"` **INFO line to os.Stdout** whenever `cfg.Handler == nil` | `config.go:109-110` | A framework that prints to the consumer's stdout at construction. Violates "no unexpected noisy output". |
| D3 | Output **hardwired to `os.Stdout`** | `config.go:75-79` | The only redirect is supplying `Config.Handler`, which bypasses filter, global fields, component, sampling, prometheus, buffer and hook. |
| D4 | `Sync()` asserts `interface{ Flush() }`; **nothing implements it** (`BufferedHandler` has no `Flush`; `MockBufferedHandler.Flush() error` has a different signature) | `slog_logger.go:110-115`, `handler/buffered_handler.go`, `handler/mocks.go:71` | `Sync()` is permanently a no-op. `ExitWithFlush` flushes nothing. **Buffered records lost on exit.** |
| D5 | `BufferedHandler.WithAttrs`/`WithGroup` call `NewBufferedHandler`, which does `go h.run()` in its constructor | `handler/buffered_handler.go:17,23,43-49` | Every `With()` on a buffered logger **leaks a goroutine + channel**. Component loggers are exactly `With()`. |
| D6 | `ComponentHandler` is inserted **unconditionally** and loops `runtime.Caller(i)` from frame 2 to 25 on **every record** | `config.go:87`, `handler/component_handler.go:40-58` | Always-on stack walking on a hot path. Its frame blacklist matches generic substrings (`/pkg/`, `/internal/`, `/handler.go`) and will misattribute or skip essentially all of Ego. |
| D7 | **No level-disabled fast path.** Every call runs `extractLogOptions` (allocates) and takes the rate-limiter mutex **before** slog checks `Enabled` | `slog_logger.go:72-85`, `rate.go:66,92-103` | A disabled `Debug` still allocates and still consumes a rate-limit token. Directly contradicts the hot-path requirement. |
| D8 | Package-level mutable globals, unprotected: `defaultLogger`, `globalContextFieldExtractor`, `once`, `exitFunc` | `config.go:13`, `context_extracto.go:8`, `loggerext.go:9,19` | `SetGlobal`/`SetContextFieldExtractor` race with concurrent logging. `SetGlobal` also calls `slog.SetDefault` (`config.go:53`), hijacking the process default. |
| D9 | `httpmw` and `grpc` subpackages hard-depend on the global `L()`; `ExitWithFlush` calls `os.Exit` | `httpmw/middleware.go:32`, `grpc/interceptor.go:29`, `loggerext.go:16` | Not usable from a library. Out of scope for Ego anyway. |
| D10 | **README is materially wrong** | wrong import path (`README:20`), wrong `Info` signature (`README:33`), claims redaction (`README:8`), wrong middleware names (`README:81-82`) | Do not design against the README. |

### 2.5 New transitive dependencies

Importing only `pkg/logger` (+ `handler`) adds **8 modules**: `prometheus/client_golang`, `prometheus/client_model`, `prometheus/common`, `prometheus/procfs`, `beorn7/perks`, `munnerz/goautoneg`, `go.yaml.in/yaml/v2`, `golang.org/x/time`.

Importing `httpmw` additionally adds `getsyntegrity/kit-core`, `oklog/ulid/v2`, `sony/sonyflake`. Importing `grpc` additionally adds `google.golang.org/grpc`, `genproto/googleapis/rpc`, `golang.org/x/text` — a large addition Ego does not carry. **Import `pkg/logger` only.**

Adopting kit-logger **does not change the zap story at all** — it neither adds nor removes zap.

---

## 3. GoAkt integration

GoAkt `v4.5.4` (`go.mod:11`). Its logger is its own package abstraction: `github.com/tochemey/goakt/v4/log`, interface `Logger` at `log/logger.go:47-141` — **20 methods**, including printf variants (`Infof`, `InfofContext`), `LogLevel() Level`, `Enabled(Level) bool`, `With(...) Logger`, `Flush() error`, `StdLogger() *golog.Logger`. Default implementations are zap-backed (`log/zap.go:43,51`). `Level` ordering is non-standard (`DebugLevel = 5`, `log/level.go:26-42`), so numeric comparison does not reflect severity — Ego already handles this with an explicit severity map (`logger.go:~130`).

Custom loggers are accepted: `actor.WithLogger(log.Logger)` (`actor/option.go:56-66`), plus `discovery/nats` and `internal/cluster`. GoAkt emits **nothing** at package init, but importing `goakt/v4/log` constructs two zap loggers as package vars — that is what pins zap.

### Two viable bridges, both already available

- **Route A (zero new code):** `goakt.WithLogger(log.NewSlogFrom(kit.Slog(), log.InfoLevel))` — `log/slog.go:104` accepts a `*slog.Logger`, and kit-logger hands one over. Caveat: GoAkt's `Slog` re-derives caller frames itself (`log/slog.go:276`), which collides with kit-logger's `ComponentHandler` (D6), and this route bypasses kit-logger's `With`-accumulated state.
- **Route B (already implemented in Ego):** keep `loggerAdapter` (`logger.go:97-279`). Since `*SlogLogger` already satisfies `ego.Logger`, `ego.WithLogger(kitLogger)` works **today** with no new code.

**Route B is the recommendation**, with two fixes to the adapter:

| GoAkt requires | kit-logger provides | Gap to close |
|---|---|---|
| `Infof/Warnf/Errorf/Debugf` | absent | Already covered by `fmt.Sprintf` in the adapter (`logger.go:212-247`). |
| `InfofContext` etc. | absent on `ego.Logger` | **Ego discards ctx today** (`logger.go:215-251`). Fix: extend `ego.Logger` (or add an optional `ContextLogger` interface) so ctx reaches kit-logger's `*Context` methods. |
| `LogLevel() Level` | **absent** (write-only `SetLevel`) | **Real bug:** kit-logger does not implement `ego.LeveledLogger { Level() string }`, so the adapter silently floors at `InfoLevel` (`logger.go:109-115`) and **GoAkt DEBUG output is lost even with kit-logger set to debug**. |
| `Enabled(Level) bool` | absent on interface, but `Slog().Enabled(ctx, lvl)` works | Route gating through `Slog().Enabled` for exact semantics. |
| `Flush() error` | `Sync() error` — and it is a no-op (D4) | Accept as no-op; document it. |
| `StdLogger()` | absent | Already covered (`logger.go:268-279`), or use `slog.NewLogLogger`. |

---

## 4. Target architecture

```text
                          context.Context  (OTel span already in ctx via telemetry.go)
                                  │
                                  │  Config.Hook: (ctx, slog.Record) -> inject trace_id/span_id
                                  ▼
                        kit-logger *SlogLogger
                        (slog core + our hardened handler chain)
                                  │
                  ┌───────────────┴────────────────┐
                  │                                │
      satisfies ego.Logger                   Slog() *slog.Logger
      (zero adapter code)                           │
                  │                                 │
                  ▼                                 ▼
        ego Config.logger  ──► loggerAdapter ──► goakt.WithLogger
        (option.go:205)        (logger.go:97)     (option.go:107)
                  │                                     │
     ┌────────────┼───────────────┐                actor system
     ▼            ▼               ▼                (cluster, discovery,
  engine     projections     persistence            actor lifecycle)
             recovery        publishers
                  │
        component loggers via With("component", ...)
                  │
                  ✗  domain model stays logger-free
                     (behavior.go, saga.go — no logger, ever)
```

Separation of concerns is preserved: **traces → OTel**, **metrics → OTel**, **logs → kit-logger**, **correlation → `context.Context`** via the `Config.Hook`. Trace correlation is **ours to build** (one hook function reading `trace.SpanContextFromContext`), not something kit-logger gives us.

### Proposed API

`WithLogger` **already exists** (`option.go:205`) and already accepts anything satisfying `ego.Logger`, which `*SlogLogger` does. So the minimum viable API is **already shipped**. What we add is ergonomics and a safe default:

```go
// Works today, no code change required:
ego.NewEngine(name, eventStore, ego.WithLogger(kitlog.New(cfg)))

// Proposed additions:
ego.NewEngine(name, eventStore)                        // safe default (see §5)
ego.NewEngine(name, eventStore, ego.WithNoLogging())    // alias for DiscardLogger
ego.NewEngine(name, eventStore, ego.WithKitLogger(l))   // opt-in, richer: ctx + component + trace hook
```

`WithKitLogger` is the honest way to get context-first logging without widening the `ego.Logger` interface for every third-party implementer — it accepts the concrete kit-logger type and can use `InfoContext`/`With`/`Slog()`. The alternative is adding an optional `ContextLogger` interface that `loggerAdapter` type-asserts, exactly like the existing `LeveledLogger` pattern (`logger.go:69-71`). **The optional-interface route is preferable**: it preserves the existing public contract, follows a pattern already in the codebase, and keeps kit-logger from becoming a compile-time requirement for consumers who bring their own logger.

---

## 5. Default logger decision

Today the default is `defaultLogger{}` → `log/slog` package functions (`logger.go:~86-93`). That is already a safe, functional, dependency-free default with predictable behaviour.

**Recommendation: keep the slog default; do NOT make kit-logger the default.** Reasons:

- D1 (Prometheus global `init()`) and D2 (unconditional stdout line) mean a kit-logger default would give every Ego consumer a mutated Prometheus registry and an unrequested stdout line, just by calling `ego.NewEngine`.
- D3 (stdout-hardwired) removes the consumer's control of output.
- The current slog default already satisfies "framework starts without configuring logging".

kit-logger becomes the **recommended, first-class, opt-in** backend, and the **default for our own services** — not a default imposed on every consumer of the framework.

---

## 6. Operational events worth logging

Hot-path sites (must stay DEBUG or be gated/sampled): per-command and per-event dispatch, `engine.go:1190,1237` per-publish INFO.

| Event | Level | Notes |
|---|---|---|
| runtime starting / ready, actor system / persistence / cluster initialized | INFO | rare, high value |
| shutdown requested, draining, outstanding ops, persistence close, runtime stopped | INFO | rare |
| aggregate activated / passivated, snapshot loaded / saved, recovery completed | DEBUG | per-entity, frequent |
| recovery failed, persistence append failed, snapshot corruption | ERROR | never sample |
| concurrent-write conflict exhausted retries | ERROR | rate-limit the retry attempts, not the exhaustion |
| projection failure, actor crash/restart, poison message | ERROR | rate-limit by key |
| cluster ownership / rebalance / discovery failure | WARN/ERROR | rate-limit reconnect storms |

Successful commands and events **must not** be INFO.

### Security

Ego is a framework; consumer payload shapes are unknown. Rule: **never** log full `command`, `event`, `state` or `snapshot` payloads — metadata, type names and IDs only. Since kit-logger has **no redaction** (2.3) and `FilterHandler` *drops whole records* rather than masking fields, redaction is **work we must build**: a `slog.Handler` decorator that masks values for a key deny-list (`password`, `token`, `access_token`, `refresh_token`, `authorization`, `cookie`, `secret`, `api_key`, `credentials`). This belongs upstream in kit-logger, not in Ego.

---

## 7. Testing and performance seams

- Test seams that construct the engine: `testkit/scenario.go`, plus the existing 529-line `logger_test.go` adapter suite (would need partial rewrite if the interface shape changes).
- Benchmarks already pass `ego.WithLogger(ego.DiscardLogger)`, deliberately keeping logging out of hot-path measurement. **Any change to the default logger must not silently pull logging back into those benchmarks.**
- D6 (always-on `runtime.Caller` loop) and D7 (no level-disabled fast path) mean a benchmark comparing `DiscardLogger` vs `kit-logger` on the command-dispatch path is **required evidence**, not optional.
- Test command: `go test -mod=vendor -p 1 -timeout 0 -race ./...`. **Requires `go mod vendor` first** (§1.5). Strict TDD mode is enabled.

---

## 8. Revised PR plan

The original A→D plan assumed greenfield. Revised against what actually exists:

| PR | Scope | Est. lines | Order |
|---|---|---|---|
| **A′ — Consolidate the existing seam** | Migrate `migration/` and `publisher/kafka` from GoAkt's `log.Logger` to `ego.Logger`; remove the `log.NewZap` default at `projection_runner.go:181`; type `projection_runner.logger` as `ego.Logger`. Public-API break on `migration.WithLogger` — needs a documented migration. | ~150-250 | first, independent |
| **B′ — Context-first + component loggers** | Add optional `ContextLogger` / `WithFields` interfaces (same pattern as `LeveledLogger`); make `loggerAdapter` stop discarding `ctx` (`logger.go:215-251`); fix the `LogLevel` flooring bug so DEBUG is not silently lost; route `Enabled` through `Slog().Enabled`. Tests. | ~200-300 | after A′ |
| **C′ — kit-logger as opt-in backend** | Add `github.com/pablogore/kit-logger` as a direct dependency, a documented constructor for a hardened `Config` (own `Handler`, no Prometheus surprise where avoidable), an OTel correlation `Hook`, component-logger helpers, integration tests. `go mod tidy` + `go mod vendor`. | ~250-350 | after B′ |
| **D′ — Migrate call sites + hardening** | Convert `engine.go`'s 8 `fmt.Sprintf` sites to structured context-first logging with the agreed field vocabulary; rate-limit retry/reconnect storms; hot-path benchmark; docs. | ~200-300 | after C′ |
| **U — Upstream kit-logger fixes** | D1, D2, D3, D4, D5, D6, D7 and the README. Belongs in the kit-logger repo, not here. | n/a | parallel, blocks C′ partially |

Every slice fits the 400-line review budget. A′ and B′ deliver real value **even if kit-logger adoption is deferred**, which is what makes this ordering safe.

---

## 9. Risks and open decisions

1. **BLOCKING DECISION — kit-logger's readiness.** D1/D2/D3/D4/D5/D6/D7 are real and verified. Making kit-logger a hard framework dependency today imports a global Prometheus registry mutation and a stdout write into every Ego consumer. This needs an explicit call: fix upstream first, adopt with a fully custom `Handler` to bypass the defect-carrying chain, or defer adoption and do A′/B′ now.
2. **Second breaking change to the same seam.** `WithLogger` already changed shape in v4.4.3 (`CHANGELOG.md:392-397`). Widening `ego.Logger` again would be a second break in one release cycle. The optional-interface pattern (§4) avoids this — strongly preferred.
3. **`migration.WithLogger` and `publisher/kafka.Config.Logger`** are public GoAkt-typed surfaces. Changing them breaks consumers and needs a documented migration.
4. **Vendoring is broken today** (§1.5). `make test` cannot run as written without an out-of-band `go mod vendor`. Independent of this change, but it blocks the validation gate.
5. **`kit-logger` has one tag, `v0.1.1`, and it is behind `main`.** Depending on it means depending on a `v0.x` pre-1.0 module with no compatibility promise. A pinned pseudo-version of `main` or a new tag is needed.
6. `UNVERIFIED`: whether kit-logger's `ComponentHandler` frame blacklist can be configured per-consumer to attribute Ego frames correctly. `offsetDepth` exists (`component_handler.go:41`) but its exported configuration path was not traced.
