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
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	goakt "github.com/tochemey/goakt/v4/actor"
	noopmetric "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	nooptrace "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/encryption"
	"github.com/getsyntegrity/urd/eventadapter"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/projection"
	"github.com/getsyntegrity/urd/tenancy"
	"github.com/getsyntegrity/urd/testkit"
)

// stubTenantResolver is a minimal tenancy.TenantResolver used across
// option_test.go and engine_test.go to exercise WithTenantResolver and
// NewEngine's tenant-resolver validation without pulling in a mocking
// framework for what is, here, just an identity comparison.
type stubTenantResolver struct {
	id string
}

var _ tenancy.TenantResolver = (*stubTenantResolver)(nil)

func (r *stubTenantResolver) Resolve(context.Context) (tenancy.TenantContext, error) {
	tid, err := tenancy.NewTenantID(r.id)
	if err != nil {
		return tenancy.TenantContext{}, err
	}
	return tenancy.NewTenantContext(tid)
}

// funcTenantResolver is a named function type implementing
// tenancy.TenantResolver, used to exercise the reflect.Func branch of
// isNilResolver: a nil value of this type is a typed-nil that must be
// treated exactly like a nil interface, not a callable resolver.
type funcTenantResolver func(context.Context) (tenancy.TenantContext, error)

var _ tenancy.TenantResolver = funcTenantResolver(nil)

func (f funcTenantResolver) Resolve(ctx context.Context) (tenancy.TenantContext, error) {
	return f(ctx)
}

// countingTenantResolver is an instrumented tenancy.TenantResolver used to
// prove exactly how many times Resolve was invoked, rather than inferring
// the count from downstream behavior. Safe for concurrent use.
type countingTenantResolver struct {
	id    string
	calls atomic.Int64
}

var _ tenancy.TenantResolver = (*countingTenantResolver)(nil)

func (r *countingTenantResolver) Resolve(context.Context) (tenancy.TenantContext, error) {
	r.calls.Add(1)
	tid, err := tenancy.NewTenantID(r.id)
	if err != nil {
		return tenancy.TenantContext{}, err
	}
	return tenancy.NewTenantContext(tid)
}

func (r *countingTenantResolver) callCount() int64 {
	return r.calls.Load()
}

// erroringTenantResolver is a tenancy.TenantResolver stub that always fails
// with a fixed error. Used to prove a resolver failure blocks a command
// before it ever reaches the actor system. It also counts its own
// invocations so tests can assert Resolve was tried exactly once.
//
// TENANT-003 T4 (corrected): Engine.Entity/DurableStateEntity/Saga no
// longer call Resolve at spawn at all (Resolve-Once, Propagate-After
// reserves Resolve for the command trust boundary alone) — a spawn under
// this resolver instead declares its tenant explicitly via engine.WithTenant,
// so this resolver can stay a simple always-fail stub with no escape hatch.
type erroringTenantResolver struct {
	err   error
	calls atomic.Int64
}

var _ tenancy.TenantResolver = (*erroringTenantResolver)(nil)

func (r *erroringTenantResolver) Resolve(context.Context) (tenancy.TenantContext, error) {
	r.calls.Add(1)
	return tenancy.TenantContext{}, r.err
}

func (r *erroringTenantResolver) callCount() int64 {
	return r.calls.Load()
}

// zeroValueTenantResolver is a tenancy.TenantResolver stub that returns the
// zero-value tenancy.TenantContext{} with a nil error — the exact malformed
// value an external TenantResolver implementation can produce, since every
// TenantContext field is unexported and a bare struct literal is the only
// construction path available outside the tenancy package. Used to prove
// Engine.SendCommand's trust boundary rejects it (design.md Decision D8)
// before dispatch, the domain handler, or persistence, rather than treating
// "no error" as "a valid identity was resolved".
//
// TENANT-003 T4 (corrected): as with erroringTenantResolver above, spawn no
// longer calls Resolve, so a spawn under this resolver declares its tenant
// via engine.WithTenant and this stub needs no escape hatch either.
type zeroValueTenantResolver struct {
	calls atomic.Int64
}

var _ tenancy.TenantResolver = (*zeroValueTenantResolver)(nil)

func (r *zeroValueTenantResolver) Resolve(context.Context) (tenancy.TenantContext, error) {
	r.calls.Add(1)
	return tenancy.TenantContext{}, nil
}

func (r *zeroValueTenantResolver) callCount() int64 {
	return r.calls.Load()
}

// perCallerTenantKey is the context key perCallerTenantResolver reads from.
type perCallerTenantKey struct{}

// perCallerTenantResolver resolves whichever tenant ID the caller placed on
// ctx under perCallerTenantKey, simulating a resolver that derives identity
// from request-scoped data (e.g. a header) rather than a fixed value. Used
// to prove concurrent commands for different tenants never cross-contaminate
// the TenantContext each command's handler observes.
type perCallerTenantResolver struct{}

var _ tenancy.TenantResolver = perCallerTenantResolver{}

func (perCallerTenantResolver) Resolve(ctx context.Context) (tenancy.TenantContext, error) {
	id, _ := ctx.Value(perCallerTenantKey{}).(string)
	tid, err := tenancy.NewTenantID(id)
	if err != nil {
		return tenancy.TenantContext{}, err
	}
	return tenancy.NewTenantContext(tid)
}

// beTheSame matches a value that is the very same pointer (or comparable
// value) as want. Deep equality would also accept a second, equal instance,
// which is not what "stores the given X" means.
func beTheSame(want any) specs.Matcher {
	return specs.Satisfy("the same instance", func(got any) bool { return got == want })
}

// beNilInterface matches an interface value that is nil itself. specs.BeNil
// also accepts a typed nil wrapped in a non-nil interface, which is exactly
// the case the typed-nil tests must tell apart.
func beNilInterface() specs.Matcher {
	return specs.Satisfy("a nil interface", func(got any) bool { return got == nil })
}

// buildActorSystem constructs and starts a goakt actor system from a Config so
// the optional extension branches in Config.GoaktOptions can be inspected. The
// system is stopped when the case ends.
func buildActorSystem(ctx *specs.Context, cfg *Config) goakt.ActorSystem {
	bg := context.Background()
	sys, err := goakt.NewActorSystem("OptionTest", cfg.GoaktOptions()...)
	ctx.Expect(err).To(specs.BeNil())
	ctx.Expect(sys.Start(bg)).To(specs.BeNil())
	ctx.Cleanup(func() { _ = sys.Stop(bg) })
	return sys
}

func TestOptionWithLogger(t *testing.T) {
	specs.Describe(t, "WithLogger sets the logger of the config", func(s *specs.Spec) {
		s.It("stores the given logger", func(ctx *specs.Context) {
			c := NewConfig(nil, WithLogger(DiscardLogger))
			ctx.Expect(c.logger).To(beTheSame(DiscardLogger))
		})
	})
}

func TestOptionWithStateStore(t *testing.T) {
	specs.Describe(t, "WithStateStore sets the durable state store of the config", func(s *specs.Spec) {
		s.It("stores the given state store", func(ctx *specs.Context) {
			store := testkit.NewDurableStore()
			c := NewConfig(nil, WithStateStore(store))
			ctx.Expect(c.stateStore).To(beTheSame(store))
		})
	})
}

func TestOptionWithOffsetStore(t *testing.T) {
	specs.Describe(t, "WithOffsetStore sets the offset store of the config", func(s *specs.Spec) {
		s.It("stores the given offset store", func(ctx *specs.Context) {
			store := testkit.NewOffsetStore()
			c := NewConfig(nil, WithOffsetStore(store))
			ctx.Expect(c.offsetStore).To(beTheSame(store))
		})
	})
}

func TestOptionWithSnapshotStore(t *testing.T) {
	specs.Describe(t, "WithSnapshotStore sets the snapshot store of the config", func(s *specs.Spec) {
		s.It("stores the given snapshot store", func(ctx *specs.Context) {
			store := testkit.NewSnapshotStore()
			c := NewConfig(nil, WithSnapshotStore(store))
			ctx.Expect(c.snapshotStore).To(beTheSame(store))
		})
	})
}

func TestOptionWithEncryptor(t *testing.T) {
	specs.Describe(t, "WithEncryptor sets the encryptor of the config", func(s *specs.Spec) {
		s.It("stores the given encryptor", func(ctx *specs.Context) {
			enc := encryption.NewAESEncryptor(testkit.NewKeyStore())
			c := NewConfig(nil, WithEncryptor(enc))
			ctx.Expect(c.encryptor).To(beTheSame(enc))
		})
	})
}

func TestOptionWithTenantResolverNil(t *testing.T) {
	specs.Describe(t, "WithTenantResolver ignores a nil resolver", func(s *specs.Spec) {
		s.It("registers nothing and leaves tenant-aware mode off", func(ctx *specs.Context) {
			// A nil resolver must be inert: no registration, no error, tenant-aware
			// mode not activated (spec.md "Nil option is inert").
			c := NewConfig(nil, WithTenantResolver(nil))
			ctx.Expect(c.tenantResolver).To(beNilInterface())
			ctx.Expect(c.tenantResolverCount).ToEqual(0)
		})
	})
}

func TestOptionWithTenantResolverTypedNil(t *testing.T) {
	specs.Describe(t, "WithTenantResolver ignores a typed-nil pointer resolver", func(s *specs.Spec) {
		s.It("treats it exactly like a plain nil", func(ctx *specs.Context) {
			// A typed-nil resolver value is non-nil at the interface level but
			// wraps a nil pointer; isNilResolver must detect it the way
			// isNilLogger does, so it is treated exactly like a plain nil.
			var typedNil *stubTenantResolver
			c := NewConfig(nil, WithTenantResolver(typedNil))
			ctx.Expect(c.tenantResolver).To(beNilInterface())
			ctx.Expect(c.tenantResolverCount).ToEqual(0)
		})
	})
}

func TestOptionWithTenantResolverFuncTypedNil(t *testing.T) {
	specs.Describe(t, "WithTenantResolver ignores a typed-nil func resolver", func(s *specs.Spec) {
		s.It("detects the nil func and registers nothing", func(ctx *specs.Context) {
			// A typed-nil value of a named function type implementing
			// tenancy.TenantResolver is non-nil at the interface level but wraps a
			// nil func; isNilResolver must detect it via the reflect.Func branch,
			// not just reflect.Pointer, or it would be registered as an "effective"
			// resolver and panic on the first Resolve call.
			var typedNil funcTenantResolver
			c := NewConfig(nil, WithTenantResolver(typedNil))
			ctx.Expect(c.tenantResolver).To(beNilInterface())
			ctx.Expect(c.tenantResolverCount).ToEqual(0)
		})
	})
}

func TestOptionWithTenantResolver(t *testing.T) {
	specs.Describe(t, "WithTenantResolver registers a non-nil resolver", func(s *specs.Spec) {
		s.It("makes a single registration the effective resolver", func(ctx *specs.Context) {
			// A single non-nil registration becomes the effective resolver and
			// activates tenant-aware mode (spec.md "Non-nil resolver registers as
			// effective").
			resolver := &stubTenantResolver{id: "acme"}
			c := NewConfig(nil, WithTenantResolver(resolver))
			ctx.Expect(c.tenantResolver).To(beTheSame(resolver))
			ctx.Expect(c.tenantResolverCount).ToEqual(1)
		})
	})
}

func TestOptionWithTenantResolverNilAfterNonNil(t *testing.T) {
	specs.Describe(t, "WithTenantResolver keeps a registered resolver when a nil follows", func(s *specs.Spec) {
		s.It("does not reset the effective resolver", func(ctx *specs.Context) {
			// nil after a valid registration must not reset it — nil is not a
			// mechanism to disable tenancy once configured (spec.md "Nil after
			// non-nil does not disable tenancy").
			resolver := &stubTenantResolver{id: "acme"}
			c := NewConfig(nil, WithTenantResolver(resolver), WithTenantResolver(nil))
			ctx.Expect(c.tenantResolver).To(beTheSame(resolver))
			ctx.Expect(c.tenantResolverCount).ToEqual(1)
		})
	})
}

func TestOptionWithTenantResolverCountsOnlyNonNilRegistrations(t *testing.T) {
	specs.Describe(t, "WithTenantResolver counts only non-nil registrations", func(s *specs.Spec) {
		s.It("counts one non-nil registration surrounded by nils as one", func(ctx *specs.Context) {
			// A single non-nil registration surrounded by nil registrations still
			// counts as exactly one (spec.md "Nil registrations do not count").
			resolver := &stubTenantResolver{id: "acme"}
			c := NewConfig(nil,
				WithTenantResolver(nil),
				WithTenantResolver(resolver),
				WithTenantResolver(nil),
			)
			ctx.Expect(c.tenantResolver).To(beTheSame(resolver))
			ctx.Expect(c.tenantResolverCount).ToEqual(1)
		})
	})
}

func TestOptionWithTenantResolverTypedNilThenValid(t *testing.T) {
	specs.Describe(t, "WithTenantResolver ignores a typed-nil registration before a valid one", func(s *specs.Spec) {
		s.It("produces exactly one effective registration", func(ctx *specs.Context) {
			// A typed-nil registration followed by a valid non-nil registration
			// must produce exactly one effective registration: the typed-nil is
			// inert and must not be mistaken for an already-registered resolver.
			var typedNil *stubTenantResolver
			resolver := &stubTenantResolver{id: "acme"}
			c := NewConfig(nil, WithTenantResolver(typedNil), WithTenantResolver(resolver))
			ctx.Expect(c.tenantResolver).To(beTheSame(resolver))
			ctx.Expect(c.tenantResolverCount).ToEqual(1)
		})
	})
}

func TestOptionWithTenantResolverValidThenTypedNil(t *testing.T) {
	specs.Describe(t, "WithTenantResolver ignores a typed-nil registration after a valid one", func(s *specs.Spec) {
		s.It("keeps the already-registered effective resolver", func(ctx *specs.Context) {
			// The reverse order must produce the same result: a typed-nil
			// registration after a valid one must not count and must not disturb
			// the already-registered effective resolver.
			var typedNil *stubTenantResolver
			resolver := &stubTenantResolver{id: "acme"}
			c := NewConfig(nil, WithTenantResolver(resolver), WithTenantResolver(typedNil))
			ctx.Expect(c.tenantResolver).To(beTheSame(resolver))
			ctx.Expect(c.tenantResolverCount).ToEqual(1)
		})
	})
}

func TestOptionWithTenantResolverAmbiguousCount(t *testing.T) {
	specs.Describe(t, "WithTenantResolver counts every distinct non-nil registration", func(s *specs.Spec) {
		s.It("counts two distinct resolvers as two", func(ctx *specs.Context) {
			// Two distinct non-nil registrations are both counted; NewEngine (not
			// the Option itself, which cannot return an error) rejects
			// tenantResolverCount > 1.
			c := NewConfig(nil,
				WithTenantResolver(&stubTenantResolver{id: "acme"}),
				WithTenantResolver(&stubTenantResolver{id: "globex"}),
			)
			ctx.Expect(c.tenantResolverCount).ToEqual(2)
		})
	})
}

func TestOptionWithProjection(t *testing.T) {
	specs.Describe(t, "WithProjection registers a projection by name", func(s *specs.Spec) {
		s.It("stores the given options under the projection name", func(ctx *specs.Context) {
			handler := projection.NewDiscardHandler()
			recovery := projection.NewRecovery(projection.WithRetries(10))
			o := &projection.Options{
				Handler:      handler,
				BufferSize:   500,
				PullInterval: time.Second,
				Recovery:     recovery,
			}
			c := NewConfig(nil, WithProjection("accounts", o))
			ctx.Expect(len(c.projections)).ToEqual(1)
			ctx.Expect(c.projections[0].name).To(specs.Equal("accounts"))
			// the registration is a copy of the caller's options
			ctx.Expect(c.projections[0].options.BufferSize).ToEqual(500)
			ctx.Expect(c.projections[0].options.PullInterval).To(specs.Equal(time.Second))
			ctx.Expect(c.projections[0].options.Recovery == recovery).To(specs.BeTrue())
		})
	})
}

func TestOptionWithProjectionMultiple(t *testing.T) {
	specs.Describe(t, "WithProjection accumulates registrations", func(s *specs.Spec) {
		s.It("keeps every projection under its own name", func(ctx *specs.Context) {
			accounts := &projection.Options{Handler: projection.NewDiscardHandler()}
			audit := &projection.Options{Handler: projection.NewDiscardHandler()}
			c := NewConfig(nil,
				WithProjection("accounts", accounts),
				WithProjection("audit", audit),
			)
			ctx.Expect(len(c.projections)).ToEqual(2)
			ctx.Expect(c.projections[0].name).To(specs.Equal("accounts"))
			ctx.Expect(c.projections[1].name).To(specs.Equal("audit"))
		})
	})
}

func TestOptionWithProjectionNil(t *testing.T) {
	specs.Describe(t, "WithProjection ignores nil options", func(s *specs.Spec) {
		s.It("registers no projection", func(ctx *specs.Context) {
			c := NewConfig(nil, WithProjection("accounts", nil))
			ctx.Expect(c.projections).To(specs.BeNil())
		})
	})
}

func TestOptionWithTelemetry(t *testing.T) {
	specs.Describe(t, "WithTelemetry sets the telemetry of the config", func(s *specs.Spec) {
		s.It("stores the given telemetry with its tracer", func(ctx *specs.Context) {
			tracer := nooptrace.NewTracerProvider().Tracer("test")
			tel := &Telemetry{Tracer: tracer}
			c := NewConfig(nil, WithTelemetry(tel))
			ctx.Expect(c.telemetry).To(specs.Not(specs.BeNil()))
			ctx.Expect(c.telemetry.Tracer).ToEqual(tracer)
		})
	})
}

func TestOptionWithTelemetryNil(t *testing.T) {
	specs.Describe(t, "WithTelemetry ignores nil telemetry", func(s *specs.Spec) {
		s.It("leaves the config without telemetry", func(ctx *specs.Context) {
			c := NewConfig(nil, WithTelemetry(nil))
			ctx.Expect(c.telemetry).To(specs.BeNil())
		})
	})
}

func TestOptionWithEventAdapters(t *testing.T) {
	specs.Describe(t, "WithEventAdapters registers event adapters", func(s *specs.Spec) {
		s.It("stores the given adapter", func(ctx *specs.Context) {
			adapter := &testEventAdapter{}
			c := NewConfig(nil, WithEventAdapters(adapter))
			ctx.Expect(c.eventAdapters).To(specs.HaveLen(1))
			ctx.Expect(c.eventAdapters[0]).ToEqual(adapter)
		})
	})
}

func TestOptionWithEventAdaptersMultiple(t *testing.T) {
	specs.Describe(t, "WithEventAdapters accumulates registrations", func(s *specs.Spec) {
		s.It("keeps the adapters of every call", func(ctx *specs.Context) {
			c := NewConfig(nil,
				WithEventAdapters(&testEventAdapter{}),
				WithEventAdapters(&testEventAdapter{}),
			)
			ctx.Expect(c.eventAdapters).To(specs.HaveLen(2))
		})
	})
}

func TestOptionWithLoggerNilFallback(t *testing.T) {
	specs.Describe(t, "WithLogger falls back to the default logger for a nil logger", func(s *specs.Spec) {
		s.It("resolves to the default logger through NewConfig", func(ctx *specs.Context) {
			// Passing a nil Logger via WithLogger should round-trip through
			// NewConfig and land on the default logger (ResolveLogger fallback).
			c := NewConfig(nil, WithLogger(nil))
			ctx.Expect(c.logger != nil).To(specs.BeTrue())
			ctx.Expect(c.logger == DefaultLogger()).To(specs.BeTrue())
		})
	})
}

func TestConfigGoaktOptionsEncryptor(t *testing.T) {
	specs.Describe(t, "Config.GoaktOptions registers the Encryptor extension", func(s *specs.Spec) {
		s.It("adds the extension when WithEncryptor is set", func(ctx *specs.Context) {
			enc := encryption.NewAESEncryptor(testkit.NewKeyStore())
			cfg := NewConfig(testkit.NewEventsStore(), WithEncryptor(enc))

			sys := buildActorSystem(ctx, cfg)
			ctx.Expect(sys.Extension(extensions.EncryptorExtensionID)).To(specs.Not(specs.BeNil()))
		})
	})
}

func TestConfigGoaktOptionsTenancyMarker(t *testing.T) {
	specs.Describe(t, "Config.GoaktOptions registers the tenancy marker extension", func(s *specs.Spec) {
		s.It("adds the marker when a non-nil resolver is configured", func(ctx *specs.Context) {
			cfg := NewConfig(testkit.NewEventsStore(), WithTenantResolver(&stubTenantResolver{id: "acme"}))

			sys := buildActorSystem(ctx, cfg)
			ctx.Expect(sys.Extension(extensions.TenancyExtensionID)).To(specs.Not(specs.BeNil()))
		})
	})
}

func TestConfigGoaktOptionsNoTenancyMarkerWithoutResolver(t *testing.T) {
	specs.Describe(t, "Config.GoaktOptions leaves the tenancy marker out without a resolver", func(s *specs.Spec) {
		s.It("carries no marker for an engine that never registers a resolver", func(ctx *specs.Context) {
			// Backward compatibility (T3/D7).
			cfg := NewConfig(testkit.NewEventsStore())

			sys := buildActorSystem(ctx, cfg)
			ctx.Expect(sys.Extension(extensions.TenancyExtensionID)).To(specs.BeNil())
		})
	})
}

func TestConfigGoaktOptionsNoTenancyMarkerWithTypedNilResolver(t *testing.T) {
	specs.Describe(t, "Config.GoaktOptions ignores typed-nil resolvers", func(s *specs.Spec) {
		s.It("carries no marker for a typed-nil pointer and a typed-nil func", func(ctx *specs.Context) {
			// A typed-nil resolver (pointer or func) is inert, not an effective
			// registration.
			var typedNilPointer *stubTenantResolver
			var typedNilFunc funcTenantResolver
			cfg := NewConfig(testkit.NewEventsStore(),
				WithTenantResolver(typedNilPointer),
				WithTenantResolver(typedNilFunc),
			)

			sys := buildActorSystem(ctx, cfg)
			ctx.Expect(sys.Extension(extensions.TenancyExtensionID)).To(specs.BeNil())
		})
	})
}

func TestConfigGoaktOptionsTelemetry(t *testing.T) {
	specs.Describe(t, "Config.GoaktOptions registers the Telemetry extension", func(s *specs.Spec) {
		s.It("adds the extension when WithTelemetry is set", func(ctx *specs.Context) {
			tel := &Telemetry{
				Tracer: nooptrace.NewTracerProvider().Tracer("test"),
				Meter:  noopmetric.NewMeterProvider().Meter("test"),
			}
			cfg := NewConfig(testkit.NewEventsStore(), WithTelemetry(tel))

			sys := buildActorSystem(ctx, cfg)
			ctx.Expect(sys.Extension(extensions.TelemetryExtensionID)).To(specs.Not(specs.BeNil()))
		})
	})
}

func TestConfigGoaktOptionsProjectionDefaultsRecovery(t *testing.T) {
	specs.Describe(t, "Config.GoaktOptions registers the projection extension", func(s *specs.Spec) {
		s.It("falls back to a default recovery when the projection has none", func(ctx *specs.Context) {
			cfg := NewConfig(testkit.NewEventsStore(),
				WithProjection("accounts", &projection.Options{
					Handler:      projection.NewDiscardHandler(),
					BufferSize:   10,
					PullInterval: time.Second,
				}),
			)

			sys := buildActorSystem(ctx, cfg)
			ctx.Expect(sys.Extension(extensions.ProjectionExtensionID)).To(specs.Not(specs.BeNil()))
		})
	})
}

// TestEngineClusterKindsExposesUrdActors pins the GoAkt kind names of the cluster
// actors. GoAkt names a kind lower(reflect.Type.String()) and ships that name
// in spawn, relocation and singleton records, so a node running another
// version only understands these exact names: the types must stay declared in
// package engine.
func TestEngineClusterKindsExposesUrdActors(t *testing.T) {
	specs.Describe(t, "ClusterKinds exposes the GoAkt kind names of the Urd actors", func(s *specs.Spec) {
		s.It("lists exactly the four cluster actors under their lower-cased type names", func(ctx *specs.Context) {
			want := []string{
				"engine.eventsourcedactor",
				"engine.durablestateactor",
				"engine.sagaactor",
				"engine.projectionactor",
			}

			var got []string
			for _, kind := range ClusterKinds() {
				got = append(got, strings.ToLower(reflect.TypeOf(kind).Elem().String()))
			}

			ctx.Expect(got).ToEqual(want)
		})
	})
}

// testEventAdapter is a no-op EventAdapter for testing.
type testEventAdapter struct{}

var _ eventadapter.EventAdapter = (*testEventAdapter)(nil)

func (a *testEventAdapter) Adapt(event *anypb.Any, _ uint64) (*anypb.Any, error) {
	return event, nil
}

// keep the trace and nooptrace packages referenced even when no test uses
// them directly.
var _ trace.Tracer = nooptrace.NewTracerProvider().Tracer("compile-check")
