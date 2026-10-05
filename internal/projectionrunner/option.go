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

package projectionrunner

import (
	"time"

	kitlog "github.com/pablogore/kit-logger/pkg/logger"

	"github.com/getsyntegrity/urd/encryption"
	"github.com/getsyntegrity/urd/eventadapter"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/instrumentation"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/projection"
)

// Option is the interface that applies a configuration option.
type Option interface {
	// Apply sets the Option value of a config.
	Apply(runner *Runner)
}

var _ Option = optionFunc(nil)

// optionFunc implements the Option interface.
type optionFunc func(*Runner)

// Apply applies the option to the runner.
func (f optionFunc) Apply(runner *Runner) {
	f(runner)
}

// WithScope sets the persistence scope the runner reads: every shard
// discovery and every events pull is restricted to it. It is required. A
// runner built without it, or with an invalid scope, refuses to Start with
// ErrScopeRequired rather than reading another scope's events or falling back
// to persistence.Unscoped().
func WithScope(scope persistence.Scope) Option {
	return optionFunc(func(runner *Runner) {
		runner.scope = scope
	})
}

// WithPullInterval sets the events pull interval
// This defines how often the projection will fetch events
func WithPullInterval(interval time.Duration) Option {
	return optionFunc(func(runner *Runner) {
		runner.pullInterval = interval
	})
}

// WithMaxBufferSize sets the max buffer size.
// This defines how many events are fetched on a single run of the projection
func WithMaxBufferSize(bufferSize int) Option {
	return optionFunc(func(runner *Runner) {
		runner.maxBufferSize = bufferSize
	})
}

// WithStartOffset sets the starting point where to read the events
func WithStartOffset(startOffset time.Time) Option {
	return optionFunc(func(runner *Runner) {
		runner.startingOffset = startOffset
	})
}

// WithResetOffset helps reset the offset to a given timestamp.
func WithResetOffset(resetOffset time.Time) Option {
	return optionFunc(func(runner *Runner) {
		runner.resetOffsetTo = resetOffset
	})
}

// WithClock replaces the real clock the runner reads time from. The clock type
// is package-private, so only this package's tests can build one: the option
// exists to drive the pull loop, the retries, the store backoff and the offset
// timestamps with a manual clock instead of sleeping. A nil clock is ignored
// and the runner keeps the real one.
func WithClock(c clock) Option {
	return optionFunc(func(runner *Runner) {
		runner.clock = c
	})
}

// WithLogger sets the kit-logger Logger the runner writes its records to.
func WithLogger(logger kitlog.Logger) Option {
	return optionFunc(func(runner *Runner) {
		runner.logger = logger
	})
}

// WithRecoveryStrategy sets the recovery strategy
func WithRecoveryStrategy(strategy *projection.Recovery) Option {
	return optionFunc(func(runner *Runner) {
		runner.recovery = strategy
	})
}

// WithDeadLetterHandler sets the dead letter handler for the projection runner
func WithDeadLetterHandler(handler projection.DeadLetterHandler) Option {
	return optionFunc(func(runner *Runner) {
		runner.deadLetterHandler = handler
	})
}

// WithEventAdapters sets the event adapters for the projection runner
func WithEventAdapters(adapters []eventadapter.EventAdapter) Option {
	return optionFunc(func(runner *Runner) {
		runner.eventAdapters = adapters
	})
}

// WithMetrics sets the metrics for the projection runner
func WithMetrics(m *instrumentation.Instruments) Option {
	return optionFunc(func(runner *Runner) {
		runner.metrics = m
	})
}

// WithEncryptor sets the encryptor for the projection runner
func WithEncryptor(enc encryption.Encryptor) Option {
	return optionFunc(func(runner *Runner) {
		runner.encryptor = enc
	})
}

// WithEventsStream sets the in-process events stream, and the topic persisted
// events are published on, that trigger an immediate pull when events are
// persisted on the local node.
func WithEventsStream(stream eventstream.Stream, topic string) Option {
	return optionFunc(func(runner *Runner) {
		runner.eventsStream = stream
		runner.eventsTopic = topic
	})
}
