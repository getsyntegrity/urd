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

package publishingtest

import (
	"context"
	"fmt"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/port/adapter/adaptertest"
	"github.com/getsyntegrity/urd/port/publishing"
)

// This package recognizes adaptertest.ErrUnreachable by its method
// instead of importing it. This test, which may import adaptertest
// (TestArchitecturePublishingtestDependsOnlyOnStdlibPublishingAndEgopb checks only the
// package's non-test imports), pins that the real sentinel, wrapped the
// way adopters wrap it, makes every check skip.
func TestCapture_RealAdaptertestErrUnreachableSkips(t *testing.T) {
	specs.Describe(t, "The capture skips every check for the real adaptertest.ErrUnreachable", func(s *specs.Spec) {
		s.It("skips all three checks when New wraps the real sentinel", func(ctx *specs.Context) {
			target := EventsTarget{
				New: func(*testing.T) (publishing.EventPublisher, error) {
					return nil, fmt.Errorf("no broker at localhost:9092: %w", adaptertest.ErrUnreachable)
				},
				Received: func(context.Context, *testing.T, *egopb.Event) error { return nil },
			}
			results := captureEvents(ctx.T, target)
			ctx.Expect(results).To(specs.HaveLen(3))
			ctx.Expect(results).To(specs.EveryElement(hasOutcome(Skipped)))
		})
	})
}
