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

package protocol

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"
)

func TestRequestToken(t *testing.T) {
	specs.Describe(t, "AttachRequestToken", func(s *specs.Spec) {
		s.It("finds no token in a context that never had one", func(ctx *specs.Context) {
			_, ok := RequestTokenFromContext(context.Background())

			ctx.Expect(ok).To(specs.BeFalse())
		})

		s.It("gives every call its own token, even for the same parent context", func(ctx *specs.Context) {
			parent := context.Background()

			first, firstOK := RequestTokenFromContext(AttachRequestToken(parent))
			second, secondOK := RequestTokenFromContext(AttachRequestToken(parent))

			ctx.Expect(firstOK).To(specs.BeTrue())
			ctx.Expect(secondOK).To(specs.BeTrue())
			ctx.Expect(first == second).To(specs.BeFalse())
		})

		s.It("keeps the token through the contexts derived from it, as GoAkt derives one per turn", func(ctx *specs.Context) {
			call := AttachRequestToken(context.Background())
			want, _ := RequestTokenFromContext(call)

			cancelable, cancel := context.WithCancel(call)
			defer cancel()
			bounded, cancelBounded := context.WithTimeout(cancelable, time.Minute)
			defer cancelBounded()

			for _, derived := range []context.Context{cancelable, bounded, context.WithoutCancel(bounded)} {
				got, ok := RequestTokenFromContext(derived)
				ctx.Expect(ok).To(specs.BeTrue())
				ctx.Expect(got == want).To(specs.BeTrue())
			}
		})

		s.It("replaces the token of a context that already has one: it is a new call", func(ctx *specs.Context) {
			first := AttachRequestToken(context.Background())
			second := AttachRequestToken(first)

			a, _ := RequestTokenFromContext(first)
			b, _ := RequestTokenFromContext(second)

			ctx.Expect(a == b).To(specs.BeFalse())
		})

		s.It("never repeats a token across concurrent callers", func(ctx *specs.Context) {
			const callers, perCaller = 8, 500
			var mu sync.Mutex
			seen := make(map[RequestToken]struct{}, callers*perCaller)

			var wg sync.WaitGroup
			for range callers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for range perCaller {
						token, _ := RequestTokenFromContext(AttachRequestToken(context.Background()))
						mu.Lock()
						seen[token] = struct{}{}
						mu.Unlock()
					}
				}()
			}
			wg.Wait()

			ctx.Expect(len(seen)).To(specs.Equal(callers * perCaller))
		})
	})
}
