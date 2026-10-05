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

// Package legacyfanin holds the capability that lets the projection actor
// subscribe to every scope of a topic.
//
// TEMPORARY (EGO-TENANT-005): the projection runner subscribes through the
// legacy, unscoped Stream API and its file belongs to #93. Until #93 gives it
// a scope, the projection actor passes it a Stream that subscribes through
// this grant. Because the package is internal, code outside this module cannot
// name Grant and therefore cannot call eventstream.EventsStream.SubscribeFanIn.
// #93 removes this package.
package legacyfanin

// Grant is the capability required to subscribe to every scope of a topic.
// Its zero value is the only value.
type Grant struct{}

// Token is the grant. Only code inside this module can reference it.
var Token = Grant{}
