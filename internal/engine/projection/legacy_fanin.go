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

package projection

import (
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/legacyfanin"
)

// fanInSubscriber is the part of the events stream that grants a subscription
// to every scope of a topic. Only *eventstream.EventsStream implements it.
type fanInSubscriber interface {
	SubscribeFanIn(sub eventstream.Subscriber, topic string, grant legacyfanin.Grant)
}

// legacyFanInStream is the Stream the projection runner is given.
//
// TEMPORARY (EGO-TENANT-005), to be removed by #93: the runner subscribes
// through the legacy unscoped Subscribe, which would receive nothing from a
// tenant-aware engine. The runner only uses the subscription as a wake-up to
// pull from the events store (which is itself scoped), so it is handed this
// wrapper, whose Subscribe registers the runner on the topic for every scope.
// Nothing else receives this wrapper: the public Subscribe and
// AddEventPublishers paths never do.
type legacyFanInStream struct {
	eventstream.Stream
	fanIn fanInSubscriber
}

// withLegacyFanIn returns stream unchanged when it cannot fan in.
func withLegacyFanIn(stream eventstream.Stream) eventstream.Stream {
	fanIn, ok := stream.(fanInSubscriber)
	if !ok {
		return stream
	}
	return &legacyFanInStream{Stream: stream, fanIn: fanIn}
}

// Subscribe registers sub on topic for every scope.
func (s *legacyFanInStream) Subscribe(sub eventstream.Subscriber, topic string) {
	if !sub.Active() {
		return
	}
	s.fanIn.SubscribeFanIn(sub, topic, legacyfanin.Token)
}
