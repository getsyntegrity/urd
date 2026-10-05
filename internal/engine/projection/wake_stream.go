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
	"github.com/getsyntegrity/urd/internal/engine/protocol"
)

// wakeStream is the Stream the projection runner is given.
//
// TEMPORARY (EGO-TENANT-005), to be removed by #93: the runner subscribes
// through the legacy Unscoped() Subscribe, which by design receives nothing a
// tenant-aware engine publishes for a tenant. The runner only uses the
// subscription as a nudge to pull from the (scoped) events store, so this
// wrapper also subscribes it to the payload-free protocol.ProjectionWakeTopic.
// Nothing else is given this wrapper; the public Subscribe, AddEventPublishers
// and AddStatePublishers paths never subscribe to that topic.
type wakeStream struct {
	eventstream.Stream
}

func withProjectionWake(stream eventstream.Stream) eventstream.Stream {
	return &wakeStream{Stream: stream}
}

// Subscribe registers sub on topic and on the wake-up topic.
func (s *wakeStream) Subscribe(sub eventstream.Subscriber, topic string) {
	s.Stream.Subscribe(sub, topic)
	s.Stream.Subscribe(sub, protocol.ProjectionWakeTopic)
}
