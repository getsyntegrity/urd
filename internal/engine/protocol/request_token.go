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
	"sync/atomic"
)

// RequestToken tells one call to an actor from every other call. It is only
// meaningful on the node that issued it: it is never serialized.
type RequestToken uint64

// requestTokenContextKey is this package's own unexported context key type for
// a RequestToken, mirroring commandCarrierContextKey.
type requestTokenContextKey struct{}

// requestTokenKey is the single key under which a RequestToken is stored in a
// context.Context by AttachRequestToken.
var requestTokenKey = requestTokenContextKey{}

// requestTokenSequence hands out the tokens. Zero is never issued.
var requestTokenSequence atomic.Uint64

// AttachRequestToken returns ctx carrying a RequestToken no other call has.
//
// An actor that has to recognise one request among others, for example to
// answer a stashed request once its write is confirmed, cannot compare the
// context it was handed: GoAkt derives a new context for every turn of an Ask,
// so the context of one turn is never the context of the next, and the actor
// must not keep it beyond the turn. A context value survives that derivation, so
// the token read in one turn is the token read in any other turn of the same
// call.
//
// Call it once per call, where the context for the Ask is built. A context that
// already carries a token gets a new one: it is a new call.
func AttachRequestToken(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestTokenKey, RequestToken(requestTokenSequence.Add(1)))
}

// RequestTokenFromContext returns the RequestToken bound to ctx by
// AttachRequestToken, or false when ctx carries none, as for an Ask that did not
// come from a call built with AttachRequestToken or that crossed a node.
func RequestTokenFromContext(ctx context.Context) (RequestToken, bool) {
	token, ok := ctx.Value(requestTokenKey).(RequestToken)
	return token, ok
}
