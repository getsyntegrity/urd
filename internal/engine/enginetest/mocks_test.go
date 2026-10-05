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

package enginetest_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/getsyntegrity/go-specs/mock"
	"github.com/getsyntegrity/go-specs/specs"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/engine/enginetest"
	"github.com/getsyntegrity/urd/persistence"
)

var errBoom = errors.New("boom")

// recordingTB is a minimal mock.TB. It records every Errorf message and keeps
// the cleanups, so a test decides when "the case ends" (finish) and can read
// what the controller reported without failing the test that runs it.
type recordingTB struct {
	errors   []string
	cleanups []func()
}

func (r *recordingTB) Helper()           {}
func (r *recordingTB) Cleanup(fn func()) { r.cleanups = append(r.cleanups, fn) }
func (r *recordingTB) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

// finish runs the registered cleanups in reverse order, as testing.T does.
func (r *recordingTB) finish() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

// reported runs use against a controller bound to a recording TB, ends the
// case and returns the messages the controller reported.
func reported(use func(c *mock.Controller)) []string {
	tb := &recordingTB{}
	use(mock.NewController(tb))
	tb.finish()
	return tb.errors
}

func TestEventsStoreMock(t *testing.T) {
	specs.Describe(t, "EventsStoreMock forwards every EventsStore method to a mock.Controller", func(s *specs.Spec) {
		bg := context.Background()
		scope := persistence.Unscoped()

		s.It("returns the scripted values, in signature order, from every method", func(ctx *specs.Context) {
			c := mock.NewController(ctx)
			store := enginetest.NewEventsStoreMock(c)
			events := []*egopb.Event{{PersistenceId: "p1"}}
			latest := &egopb.Event{PersistenceId: "p2"}

			c.Method("Connect").Expect(mock.Any()).Return(nil)
			c.Method("Disconnect").Expect(mock.Any()).Return(nil)
			c.Method("Ping").Expect(mock.Any()).Return(nil)
			c.Method("WriteEvents").Expect(bg, scope, events, persistence.ExpectRevision(3)).Return(nil)
			c.Method("DeleteEvents").Expect(bg, scope, "p1", uint64(9)).Return(nil)
			c.Method("ReplayEvents").Expect(bg, scope, "p1", uint64(1), uint64(5), uint64(10)).Return(events, nil)
			c.Method("GetLatestEvent").Expect(bg, scope, "p2").Return(latest, nil)
			c.Method("PersistenceIDs").Expect(bg, scope, uint64(20), "tok").Return([]string{"a", "b"}, "next", nil)
			c.Method("GetShardEvents").Expect(bg, persistence.Unscoped(), uint64(4), int64(7), uint64(50)).Return(events, int64(8), nil)
			c.Method("ShardOffsets").Expect(bg, persistence.Unscoped()).Return(map[uint64]int64{4: 8}, nil)

			ctx.Expect(store.Connect(bg)).To(specs.BeNil())
			ctx.Expect(store.Disconnect(bg)).To(specs.BeNil())
			ctx.Expect(store.Ping(bg)).To(specs.BeNil())
			ctx.Expect(store.WriteEvents(bg, scope, events, persistence.ExpectRevision(3))).To(specs.BeNil())
			ctx.Expect(store.DeleteEvents(bg, scope, "p1", 9)).To(specs.BeNil())

			replayed, err := store.ReplayEvents(bg, scope, "p1", 1, 5, 10)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(replayed).To(specs.Equal(events))

			gotLatest, err := store.GetLatestEvent(bg, scope, "p2")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(gotLatest).To(specs.Equal(latest))

			ids, next, err := store.PersistenceIDs(bg, scope, 20, "tok")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(ids).To(specs.Equal([]string{"a", "b"}))
			ctx.Expect(next).To(specs.Equal("next"))

			shardEvents, shardOffset, err := store.GetShardEvents(bg, persistence.Unscoped(), 4, 7, 50)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(shardEvents).To(specs.Equal(events))
			ctx.Expect(shardOffset).To(specs.Equal(int64(8)))

			offsets, err := store.ShardOffsets(bg, persistence.Unscoped())
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(offsets).To(specs.Equal(map[uint64]int64{4: 8}))
		})

		s.It("returns the scripted error from the methods that return one", func(ctx *specs.Context) {
			c := mock.NewController(ctx)
			store := enginetest.NewEventsStoreMock(c)
			c.Method("WriteEvents").Expect(mock.Any(), mock.Any(), mock.Any(), mock.Any()).Return(errBoom)
			c.Method("PersistenceIDs").Expect(mock.Any(), mock.Any(), mock.Any(), mock.Any()).Return(nil, "", errBoom)

			ctx.Expect(store.WriteEvents(bg, scope, nil, persistence.Unconditional())).To(specs.MatchError(errBoom))
			ids, next, err := store.PersistenceIDs(bg, scope, 1, "")
			ctx.Expect(err).To(specs.MatchError(errBoom))
			ctx.Expect(ids).To(specs.BeNil())
			ctx.Expect(next).To(specs.Equal(""))
		})

		s.It("reports an expectation nobody met and a call nobody expected", func(ctx *specs.Context) {
			msgs := reported(func(c *mock.Controller) {
				c.Method("Ping").Expect(mock.Any())
				_ = enginetest.NewEventsStoreMock(c).Connect(bg)
			})
			ctx.Expect(msgs).To(specs.AnyElement(specs.MatchRegex(`unexpected call Connect`)))
			ctx.Expect(msgs).To(specs.AnyElement(specs.MatchRegex(`Ping`)))
		})
	})
}

func TestSnapshotStoreMock(t *testing.T) {
	specs.Describe(t, "SnapshotStoreMock forwards every SnapshotStore method to a mock.Controller", func(s *specs.Spec) {
		bg := context.Background()
		scope := persistence.Unscoped()

		s.It("returns the scripted values, in signature order, from every method", func(ctx *specs.Context) {
			c := mock.NewController(ctx)
			store := enginetest.NewSnapshotStoreMock(c)
			snapshot := &egopb.Snapshot{PersistenceId: "p1"}

			c.Method("Connect").Expect(mock.Any()).Return(nil)
			c.Method("Disconnect").Expect(mock.Any()).Return(nil)
			c.Method("Ping").Expect(mock.Any()).Return(nil)
			c.Method("WriteSnapshot").Expect(bg, scope, snapshot).Return(nil)
			c.Method("GetLatestSnapshot").Expect(bg, scope, "p1").Return(snapshot, nil)
			c.Method("DeleteSnapshots").Expect(bg, scope, "p1", uint64(9)).Return(nil)

			ctx.Expect(store.Connect(bg)).To(specs.BeNil())
			ctx.Expect(store.Disconnect(bg)).To(specs.BeNil())
			ctx.Expect(store.Ping(bg)).To(specs.BeNil())
			ctx.Expect(store.WriteSnapshot(bg, scope, snapshot)).To(specs.BeNil())
			got, err := store.GetLatestSnapshot(bg, scope, "p1")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.Equal(snapshot))
			ctx.Expect(store.DeleteSnapshots(bg, scope, "p1", 9)).To(specs.BeNil())
		})

		s.It("returns the scripted error from the methods that return one", func(ctx *specs.Context) {
			c := mock.NewController(ctx)
			store := enginetest.NewSnapshotStoreMock(c)
			c.Method("GetLatestSnapshot").Expect(mock.Any(), mock.Any(), mock.Any()).Return(nil, errBoom)

			got, err := store.GetLatestSnapshot(bg, scope, "p1")
			ctx.Expect(err).To(specs.MatchError(errBoom))
			ctx.Expect(got).To(specs.BeNil())
		})

		s.It("reports an expectation nobody met", func(ctx *specs.Context) {
			msgs := reported(func(c *mock.Controller) {
				c.Method("GetLatestSnapshot").Expect(mock.Any(), mock.Any(), mock.Any())
				_ = enginetest.NewSnapshotStoreMock(c)
			})
			ctx.Expect(msgs).To(specs.AnyElement(specs.MatchRegex(`GetLatestSnapshot`)))
		})
	})
}

func TestStateStoreMock(t *testing.T) {
	specs.Describe(t, "StateStoreMock forwards every StateStore method to a mock.Controller", func(s *specs.Spec) {
		bg := context.Background()
		scope := persistence.Unscoped()

		s.It("returns the scripted values, in signature order, from every method", func(ctx *specs.Context) {
			c := mock.NewController(ctx)
			store := enginetest.NewStateStoreMock(c)
			state := &egopb.DurableState{PersistenceId: "p1"}

			c.Method("Connect").Expect(mock.Any()).Return(nil)
			c.Method("Disconnect").Expect(mock.Any()).Return(nil)
			c.Method("Ping").Expect(mock.Any()).Return(nil)
			c.Method("WriteState").Expect(bg, scope, state, persistence.ExpectGenesis()).Return(nil)
			c.Method("GetLatestState").Expect(bg, scope, "p1").Return(state, nil)

			ctx.Expect(store.Connect(bg)).To(specs.BeNil())
			ctx.Expect(store.Disconnect(bg)).To(specs.BeNil())
			ctx.Expect(store.Ping(bg)).To(specs.BeNil())
			ctx.Expect(store.WriteState(bg, scope, state, persistence.ExpectGenesis())).To(specs.BeNil())
			got, err := store.GetLatestState(bg, scope, "p1")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.Equal(state))
		})

		s.It("returns the scripted error from the methods that return one", func(ctx *specs.Context) {
			c := mock.NewController(ctx)
			store := enginetest.NewStateStoreMock(c)
			c.Method("WriteState").Expect(mock.Any(), mock.Any(), mock.Any(), mock.Any()).Return(errBoom)
			c.Method("GetLatestState").Expect(mock.Any(), mock.Any(), mock.Any()).Return(nil, errBoom)

			ctx.Expect(store.WriteState(bg, scope, &egopb.DurableState{}, persistence.Unconditional())).To(specs.MatchError(errBoom))
			got, err := store.GetLatestState(bg, scope, "p1")
			ctx.Expect(err).To(specs.MatchError(errBoom))
			ctx.Expect(got).To(specs.BeNil())
		})

		s.It("reports an expectation nobody met", func(ctx *specs.Context) {
			msgs := reported(func(c *mock.Controller) {
				c.Method("GetLatestState").Expect(mock.Any(), mock.Any(), mock.Any())
				_ = enginetest.NewStateStoreMock(c)
			})
			ctx.Expect(msgs).To(specs.AnyElement(specs.MatchRegex(`GetLatestState`)))
		})
	})
}

func TestEncryptorMock(t *testing.T) {
	specs.Describe(t, "EncryptorMock forwards Encrypt and Decrypt to a mock.Controller", func(s *specs.Spec) {
		bg := context.Background()

		s.It("returns the scripted values, in signature order, from both methods", func(ctx *specs.Context) {
			c := mock.NewController(ctx)
			enc := enginetest.NewEncryptorMock(c)
			c.Method("Encrypt").Expect(bg, "p1", []byte("plain")).Return([]byte("cipher"), "key-1", nil)
			c.Method("Decrypt").Expect(bg, "p1", []byte("cipher"), "key-1").Return([]byte("plain"), nil)

			cipher, keyID, err := enc.Encrypt(bg, "p1", []byte("plain"))
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(cipher).To(specs.Equal([]byte("cipher")))
			ctx.Expect(keyID).To(specs.Equal("key-1"))

			plain, err := enc.Decrypt(bg, "p1", []byte("cipher"), "key-1")
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(plain).To(specs.Equal([]byte("plain")))
		})

		s.It("returns the scripted error from both methods", func(ctx *specs.Context) {
			c := mock.NewController(ctx)
			enc := enginetest.NewEncryptorMock(c)
			c.Method("Encrypt").Expect(mock.Any(), mock.Any(), mock.Any()).Return(nil, "", errBoom)
			c.Method("Decrypt").Expect(mock.Any(), mock.Any(), mock.Any(), mock.Any()).Return(nil, errBoom)

			cipher, keyID, err := enc.Encrypt(bg, "p1", nil)
			ctx.Expect(err).To(specs.MatchError(errBoom))
			ctx.Expect(cipher).To(specs.BeNil())
			ctx.Expect(keyID).To(specs.Equal(""))

			plain, err := enc.Decrypt(bg, "p1", nil, "k")
			ctx.Expect(err).To(specs.MatchError(errBoom))
			ctx.Expect(plain).To(specs.BeNil())
		})

		s.It("reports an expectation nobody met", func(ctx *specs.Context) {
			msgs := reported(func(c *mock.Controller) {
				c.Method("Encrypt").Expect(mock.Any(), mock.Any(), mock.Any())
				_ = enginetest.NewEncryptorMock(c)
			})
			ctx.Expect(msgs).To(specs.AnyElement(specs.MatchRegex(`Encrypt`)))
		})
	})
}

func TestEventAdapterMock(t *testing.T) {
	specs.Describe(t, "EventAdapterMock forwards Adapt to a mock.Controller", func(s *specs.Spec) {
		s.It("returns the scripted event", func(ctx *specs.Context) {
			c := mock.NewController(ctx)
			adapter := enginetest.NewEventAdapterMock(c)
			in, out := &anypb.Any{TypeUrl: "old"}, &anypb.Any{TypeUrl: "new"}
			c.Method("Adapt").Expect(in, uint64(2)).Return(out, nil)

			got, err := adapter.Adapt(in, 2)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.Equal(out))
		})

		s.It("returns the scripted error", func(ctx *specs.Context) {
			c := mock.NewController(ctx)
			adapter := enginetest.NewEventAdapterMock(c)
			c.Method("Adapt").Expect(mock.Any(), mock.Any()).Return(nil, errBoom)

			got, err := adapter.Adapt(&anypb.Any{}, 1)
			ctx.Expect(err).To(specs.MatchError(errBoom))
			ctx.Expect(got).To(specs.BeNil())
		})

		s.It("reports an expectation nobody met", func(ctx *specs.Context) {
			msgs := reported(func(c *mock.Controller) {
				c.Method("Adapt").Expect(mock.Any(), mock.Any())
				_ = enginetest.NewEventAdapterMock(c)
			})
			ctx.Expect(msgs).To(specs.AnyElement(specs.MatchRegex(`Adapt`)))
		})
	})
}

func TestOffsetStoreMock(t *testing.T) {
	specs.Describe(t, "OffsetStoreMock forwards every OffsetStore method to a mock.Controller", func(s *specs.Spec) {
		bg := context.Background()

		s.It("returns the scripted values, in signature order, from every method", func(ctx *specs.Context) {
			c := mock.NewController(ctx)
			store := enginetest.NewOffsetStoreMock(c)
			id := &egopb.ProjectionId{ProjectionName: "p"}
			offset := &egopb.Offset{ProjectionName: "p", Value: 7}

			c.Method("Connect").Expect(mock.Any()).Return(nil)
			c.Method("Disconnect").Expect(mock.Any()).Return(nil)
			c.Method("Ping").Expect(mock.Any()).Return(nil)
			c.Method("WriteOffset").Expect(bg, offset).Return(nil)
			c.Method("GetCurrentOffset").Expect(bg, id).Return(offset, nil)
			c.Method("ResetOffset").Expect(bg, "p", int64(3)).Return(nil)

			ctx.Expect(store.Connect(bg)).To(specs.BeNil())
			ctx.Expect(store.Disconnect(bg)).To(specs.BeNil())
			ctx.Expect(store.Ping(bg)).To(specs.BeNil())
			ctx.Expect(store.WriteOffset(bg, offset)).To(specs.BeNil())
			got, err := store.GetCurrentOffset(bg, id)
			ctx.Expect(err).To(specs.BeNil())
			ctx.Expect(got).To(specs.Equal(offset))
			ctx.Expect(store.ResetOffset(bg, "p", 3)).To(specs.BeNil())
		})

		s.It("returns the scripted error", func(ctx *specs.Context) {
			c := mock.NewController(ctx)
			store := enginetest.NewOffsetStoreMock(c)
			c.Method("GetCurrentOffset").Expect(mock.Any(), mock.Any()).Return(nil, errBoom)

			got, err := store.GetCurrentOffset(bg, &egopb.ProjectionId{})
			ctx.Expect(err).To(specs.MatchError(errBoom))
			ctx.Expect(got).To(specs.BeNil())
		})
	})
}

func TestPublisherMocks(t *testing.T) {
	specs.Describe(t, "the publisher mocks forward ID, Publish and Close to a mock.Controller", func(s *specs.Spec) {
		bg := context.Background()

		s.It("EventPublisherMock returns the scripted values", func(ctx *specs.Context) {
			c := mock.NewController(ctx)
			pub := enginetest.NewEventPublisherMock(c)
			event := &egopb.Event{PersistenceId: "p1"}
			c.Method("ID").Expect().Return("events")
			c.Method("Publish").Expect(bg, event).Return(errBoom)
			c.Method("Close").Expect(bg).Return(nil)

			ctx.Expect(pub.ID()).ToEqual("events")
			ctx.Expect(pub.Publish(bg, event)).To(specs.MatchError(errBoom))
			ctx.Expect(pub.Close(bg)).To(specs.BeNil())
		})

		s.It("StatePublisherMock returns the scripted values", func(ctx *specs.Context) {
			c := mock.NewController(ctx)
			pub := enginetest.NewStatePublisherMock(c)
			state := &egopb.DurableState{PersistenceId: "p1"}
			c.Method("ID").Expect().Return("states")
			c.Method("Publish").Expect(bg, state).Return(errBoom)
			c.Method("Close").Expect(bg).Return(nil)

			ctx.Expect(pub.ID()).ToEqual("states")
			ctx.Expect(pub.Publish(bg, state)).To(specs.MatchError(errBoom))
			ctx.Expect(pub.Close(bg)).To(specs.BeNil())
		})
	})
}
