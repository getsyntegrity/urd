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

package extensions

import (
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/tochemey/goakt/v4/extension"
	"go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/getsyntegrity/urd/encryption"
	"github.com/getsyntegrity/urd/eventadapter"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/projection"
	"github.com/getsyntegrity/urd/testkit"
)

// beTheSamePointer matches a value that is the very pointer want. ToEqual
// compares deeply, so it would also accept a copy with the same contents.
func beTheSamePointer[T any](want *T) specs.Matcher {
	return specs.Satisfy("be the same pointer as the registered value", func(got any) bool {
		p, ok := got.(*T)
		return ok && p == want
	})
}

func TestEventsStore(t *testing.T) {
	specs.Describe(t, "NewEventsStore wraps an events store as a GoAkt extension", func(s *specs.Spec) {
		s.It("exposes its ID and the wrapped store", func(ctx *specs.Context) {
			store := testkit.NewEventsStore()
			ext := NewEventsStore(store)

			ctx.Expect(ext).To(specs.Not(specs.BeNil()))
			ctx.Expect(ext.ID()).ToEqual(EventsStoreExtensionID)
			ctx.Expect(ext.Underlying()).ToEqual(store)
		})
	})
}

func TestDurableStateStore(t *testing.T) {
	specs.Describe(t, "NewDurableStateStore wraps a durable state store as a GoAkt extension", func(s *specs.Spec) {
		s.It("exposes its ID and the wrapped store", func(ctx *specs.Context) {
			store := testkit.NewDurableStore()
			ext := NewDurableStateStore(store)

			ctx.Expect(ext).To(specs.Not(specs.BeNil()))
			ctx.Expect(ext.ID()).ToEqual(DurableStateStoreExtensionID)
			ctx.Expect(ext.Underlying()).ToEqual(store)
		})
	})
}

func TestEventsStream(t *testing.T) {
	specs.Describe(t, "NewEventsStream wraps an events stream as a GoAkt extension", func(s *specs.Spec) {
		s.It("exposes its ID and the wrapped stream", func(ctx *specs.Context) {
			stream := eventstream.New()
			ext := NewEventsStream(stream)

			ctx.Expect(ext).To(specs.Not(specs.BeNil()))
			ctx.Expect(ext.ID()).ToEqual(EventsStreamExtensionID)
			ctx.Expect(ext.Underlying()).ToEqual(stream)
		})
	})
}

func TestOffsetStore(t *testing.T) {
	specs.Describe(t, "NewOffsetStore wraps an offset store as a GoAkt extension", func(s *specs.Spec) {
		s.It("exposes its ID and the wrapped store", func(ctx *specs.Context) {
			store := testkit.NewOffsetStore()
			ext := NewOffsetStore(store)

			ctx.Expect(ext).To(specs.Not(specs.BeNil()))
			ctx.Expect(ext.ID()).ToEqual(OffsetStoreExtensionID)
			ctx.Expect(ext.Underlying()).ToEqual(store)
		})
	})
}

func TestProjectionExtension(t *testing.T) {
	specs.Describe(t, "NewProjectionExtension serves projection options by name", func(s *specs.Spec) {
		s.It("returns the registered options and nil for an unknown name", func(ctx *specs.Context) {
			accounts := &projection.Options{
				Handler:           projection.NewDiscardHandler(),
				BufferSize:        100,
				StartOffset:       time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
				ResetOffset:       time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC),
				PullInterval:      500 * time.Millisecond,
				Recovery:          projection.NewRecovery(),
				DeadLetterHandler: projection.NewDiscardDeadLetterHandler(),
			}
			audit := &projection.Options{
				Handler: projection.NewDiscardHandler(),
			}

			ext := NewProjectionExtension(map[string]*projection.Options{
				"accounts": accounts,
				"audit":    audit,
			})

			ctx.Expect(ext).To(specs.Not(specs.BeNil()))
			ctx.Expect(ext.ID()).ToEqual(ProjectionExtensionID)
			ctx.Expect(ext.Get("accounts")).To(beTheSamePointer(accounts))
			ctx.Expect(ext.Get("audit")).To(beTheSamePointer(audit))
			ctx.Expect(ext.Get("unknown")).To(specs.BeNil())
		})
	})
}

func TestEventAdapters(t *testing.T) {
	specs.Describe(t, "NewEventAdapters wraps event adapters as a GoAkt extension", func(s *specs.Spec) {
		s.It("with nil adapters", func(ctx *specs.Context) {
			ext := NewEventAdapters(nil)

			ctx.Expect(ext).To(specs.Not(specs.BeNil()))
			ctx.Expect(ext.ID()).ToEqual(EventAdaptersExtensionID)
			ctx.Expect(ext.Adapters()).To(specs.BeNil())
		})

		s.It("with empty adapters", func(ctx *specs.Context) {
			adapters := []eventadapter.EventAdapter{}
			ext := NewEventAdapters(adapters)

			ctx.Expect(ext).To(specs.Not(specs.BeNil()))
			ctx.Expect(ext.ID()).ToEqual(EventAdaptersExtensionID)
			ctx.Expect(ext.Adapters()).To(specs.BeEmpty())
		})
	})
}

func TestSnapshotStoreExt(t *testing.T) {
	specs.Describe(t, "NewSnapshotStore wraps a snapshot store as a GoAkt extension", func(s *specs.Spec) {
		s.It("exposes its ID and the wrapped store", func(ctx *specs.Context) {
			store := testkit.NewSnapshotStore()
			ext := NewSnapshotStore(store)

			ctx.Expect(ext).To(specs.Not(specs.BeNil()))
			ctx.Expect(ext.ID()).ToEqual(SnapshotStoreExtensionID)
			ctx.Expect(ext.Underlying()).ToEqual(store)
		})
	})
}

func TestEncryptorExtension(t *testing.T) {
	specs.Describe(t, "NewEncryptor wraps an encryptor as a GoAkt extension", func(s *specs.Spec) {
		s.It("exposes its ID and the wrapped encryptor", func(ctx *specs.Context) {
			ks := testkit.NewKeyStore()
			enc := encryption.NewAESEncryptor(ks)
			ext := NewEncryptor(enc)

			ctx.Expect(ext).To(specs.Not(specs.BeNil()))
			ctx.Expect(ext.ID()).ToEqual(EncryptorExtensionID)
			ctx.Expect(ext.Encryptor()).ToEqual(enc)
		})
	})
}

func TestTenancyMarker(t *testing.T) {
	specs.Describe(t, "NewTenancyMarker builds the tenancy marker extension", func(s *specs.Spec) {
		s.It("exposes the tenancy extension ID", func(ctx *specs.Context) {
			ext := NewTenancyMarker(false)

			ctx.Expect(ext).To(specs.Not(specs.BeNil()))
			ctx.Expect(ext.ID()).ToEqual(TenancyExtensionID)
		})
	})
}

func TestEntityConfig(t *testing.T) {
	specs.Describe(t, "NewEntityConfig builds the entity configuration extension", func(s *specs.Spec) {
		var cfg *EntityConfig

		// Shared setup: the round-trip case mutates cfg, so each case gets a fresh one.
		s.BeforeEach(func(ctx *specs.Context) {
			cfg = NewEntityConfig(10)
		})

		s.It("exposes its ID and snapshot interval", func(ctx *specs.Context) {
			ctx.Expect(cfg).To(specs.Not(specs.BeNil()))
			ctx.Expect(cfg.ID()).ToEqual(EntityConfigID)
			ctx.Expect(cfg.SnapshotInterval).ToEqual(uint64(10))
		})

		s.It("marshal and unmarshal round-trip", func(ctx *specs.Context) {
			cfg.DeleteEventsOnSnapshot = true
			cfg.DeleteSnapshotsOnSnapshot = true
			cfg.EventsRetentionCount = 50
			cfg.HasRetentionPolicy = true

			data, err := cfg.MarshalBinary()
			ctx.Expect(err).To(specs.BeNil())

			cfg2 := &EntityConfig{}
			err = cfg2.UnmarshalBinary(data)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(cfg2).ToEqual(cfg)
		})
	})
}

func TestSagaConfig(t *testing.T) {
	specs.Describe(t, "NewSagaConfig builds the saga configuration extension", func(s *specs.Spec) {
		var cfg *SagaConfig

		s.BeforeEach(func(ctx *specs.Context) {
			cfg = NewSagaConfig(5 * time.Second)
		})

		s.It("exposes its ID and timeout", func(ctx *specs.Context) {
			ctx.Expect(cfg).To(specs.Not(specs.BeNil()))
			ctx.Expect(cfg.ID()).ToEqual(SagaConfigID)
			ctx.Expect(cfg.Timeout).ToEqual(5 * time.Second)
		})

		s.It("marshal and unmarshal round-trip", func(ctx *specs.Context) {
			data, err := cfg.MarshalBinary()
			ctx.Expect(err).To(specs.BeNil())

			cfg2 := &SagaConfig{}
			err = cfg2.UnmarshalBinary(data)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(cfg2).ToEqual(cfg)
		})
	})
}

func TestTelemetryExtension(t *testing.T) {
	specs.Describe(t, "NewTelemetryExtension wraps a tracer and a meter as a GoAkt extension", func(s *specs.Spec) {
		s.It("exposes its ID, tracer and meter", func(ctx *specs.Context) {
			tracer := tracenoop.NewTracerProvider().Tracer("test")
			meter := noop.NewMeterProvider().Meter("test")

			ext := NewTelemetryExtension(tracer, meter)

			ctx.Expect(ext).To(specs.Not(specs.BeNil()))
			ctx.Expect(ext.ID()).ToEqual(TelemetryExtensionID)
			ctx.Expect(ext.Tracer()).ToEqual(tracer)
			ctx.Expect(ext.Meter()).ToEqual(meter)
		})
	})
}

// localBehaviorProbe is a behavior with only an ID, standing in for a
// domain-only behavior that has no serialization methods.
type localBehaviorProbe struct{ id string }

func (p localBehaviorProbe) ID() string { return p.id }

func TestLocalBehavior(t *testing.T) {
	specs.Describe(t, "NewLocalBehavior carries a behavior that is never serialized", func(s *specs.Spec) {
		s.It("keeps the behavior's ID and refuses serialization", func(ctx *specs.Context) {
			probe := localBehaviorProbe{id: "entity-1"}
			local := NewLocalBehavior(probe)

			// The dependency key must stay the behavior's ID.
			var dep extension.Dependency = local
			ctx.Expect(dep.ID()).ToEqual("entity-1")
			ctx.Expect(local.Behavior()).ToEqual(probe)

			// A LocalBehavior is never serialized: a GoAkt remote dependency query
			// against an actor carrying one gets this error back instead of bytes.
			data, err := local.MarshalBinary()
			ctx.Expect(err).To(specs.MatchError(errLocalOnly))
			ctx.Expect(data).To(specs.BeNil())
			ctx.Expect(local.UnmarshalBinary([]byte("x"))).To(specs.MatchError(errLocalOnly))
		})
	})
}
