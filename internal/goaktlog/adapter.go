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

// Package goaktlog presents a kit-logger Logger to the GoAkt actor runtime
// through GoAkt's own log.Logger interface.
//
// GoAkt is the actor runtime Urd sits on and cannot take a kit-logger Logger
// directly, so this package is the single seam where GoAkt's printf-style
// logging API is translated into structured kit-logger records. It is the
// only first-party package allowed to import github.com/tochemey/goakt/v4/log
// (see TestArchitectureKitLoggerIsTheOnlyLoggingBackend in package engine).
//
// It is GoAkt-specific on purpose: the runtime-neutral logger resolution —
// the process-wide default and the nil and typed-nil fallback — stays in
// internal/logging, which imports neither this package nor GoAkt, so
// packages such as migration can resolve a logger without the runtime.
// The dependency direction is engine -> goaktlog -> logging, never back.
package goaktlog

import (
	"context"
	"fmt"
	golog "log"
	"log/slog"
	"strings"

	kitlog "github.com/pablogore/kit-logger/pkg/logger"
	"github.com/tochemey/goakt/v4/log"

	"github.com/getsyntegrity/urd/internal/logging"
)

// adapter presents a kit-logger Logger through GoAkt's log.Logger
// interface, which is what the actor system and every actor context expose.
//
// The adapter is deliberately thin: kit-logger already offers context-aware
// methods, native child loggers and a level gate, so every GoAkt call maps to
// exactly one kit-logger call. The only translation it performs is splitting
// GoAkt's variadic arguments into a message and its key-value fields, and
// rendering the printf-style variants — after checking the level, so a record
// nobody will emit is never formatted.
type adapter struct {
	// backend is the kit-logger Logger the application configured, exactly as
	// it was given. It is what Backend hands back to Urd's own actors,
	// which call it directly and must be attributed to their own call sites.
	backend kitlog.Logger

	// inner is backend adjusted for GoAkt's records. Every GoAkt call passes
	// through one adapter method before reaching kit-logger, so when the
	// backend supports it, inner skips that frame and a record names the
	// GoAkt call site instead of this file.
	inner kitlog.Logger

	// slogger is the backend's own *slog.Logger, resolved once: it answers
	// every level check without a round trip through kit-logger's variadic
	// argument scanning, and it shares the backend's level variable, so a
	// SetLevel made at runtime is honored on the very next check.
	slogger *slog.Logger
}

// compile-time check
var _ log.Logger = (*adapter)(nil)

// New wraps a kit-logger Logger for GoAkt. The engine hands the result to
// goakt.WithLogger, so the actor system and every actor context log through
// the backend the application configured. backend must already be resolved
// (see logging.ResolveLogger): New does not apply the nil fallback itself.
func New(backend kitlog.Logger) log.Logger {
	return newAdapter(backend)
}

// newAdapter wraps a kit-logger Logger for GoAkt.
func newAdapter(backend kitlog.Logger) *adapter {
	inner := backend
	if skipper, ok := backend.(kitlog.CallerSkipper); ok {
		inner = skipper.WithCallerSkip(1)
	}
	return &adapter{backend: backend, inner: inner, slogger: backend.Slog()}
}

// Backend recovers the kit-logger Logger behind a GoAkt logger. Every
// actor system built from Config.GoaktOptions carries an adapter, so
// Urd's own actors log through the same backend the application configured.
// A foreign GoAkt logger — an actor system assembled without GoaktOptions —
// yields logging.DefaultLogger(), which is the same fallback the engine
// applies when no logger is configured.
func Backend(l log.Logger) kitlog.Logger {
	if a, ok := l.(*adapter); ok {
		return a.backend
	}
	return logging.DefaultLogger()
}

// goaktLevelsByVerbosity lists every goakt log.Level from most to least
// verbose. LogLevel walks it to report the most verbose enabled level.
var goaktLevelsByVerbosity = []log.Level{
	log.DebugLevel,
	log.InfoLevel,
	log.WarningLevel,
	log.ErrorLevel,
	log.FatalLevel,
	log.PanicLevel,
}

// goaktToSlogLevel maps a goakt log.Level to the slog.Level the backend is
// asked about. GoAkt's level enum has non-standard ordering (DebugLevel is the
// last iota value), so the mapping is explicit rather than a numeric cast.
// Fatal and Panic sit above Error to keep the result strictly monotonic in
// severity, which lets LogLevel probe levels in verbosity order.
func goaktToSlogLevel(l log.Level) slog.Level {
	switch l {
	case log.DebugLevel:
		return slog.LevelDebug
	case log.InfoLevel:
		return slog.LevelInfo
	case log.WarningLevel:
		return slog.LevelWarn
	case log.ErrorLevel:
		return slog.LevelError
	case log.FatalLevel:
		return slog.LevelError + 4
	case log.PanicLevel:
		return slog.LevelError + 8
	default:
		return slog.LevelInfo
	}
}

// enabledAt asks the backend whether it accepts level for the given context.
// A backend whose Slog returns nil declared no level, so every level reports
// enabled: the adapter never filters on behalf of a Logger that never said it
// wanted filtering.
func (a *adapter) enabledAt(ctx context.Context, l log.Level) bool {
	if a.slogger == nil {
		return true
	}
	return a.slogger.Enabled(ctx, goaktToSlogLevel(l))
}

// goaktArgsToMsg splits GoAkt variadic log arguments into a message string
// and optional key-value fields. GoAkt follows the slog convention where the
// first argument is the log message and subsequent arguments are alternating
// key-value pairs for structured logging.
func goaktArgsToMsg(args []any) (string, []any) {
	if len(args) == 0 {
		return "", nil
	}

	msg, ok := args[0].(string) // the common case — avoids fmt.Sprint reflection
	if !ok {
		msg = fmt.Sprint(args[0])
	}

	if len(args) == 1 {
		return msg, nil
	}
	return msg, args[1:]
}

// The plain methods do not re-check the level: GoAkt already guards its call
// sites with Enabled, and kit-logger applies its own gate before formatting.
// The printf-style methods do check, because the fmt.Sprintf allocation is one
// this adapter would otherwise pay for a record nobody will emit.

func (a *adapter) Debug(args ...any) {
	msg, fields := goaktArgsToMsg(args)
	a.inner.Debug(msg, fields...)
}
func (a *adapter) Debugf(format string, args ...any) {
	if !a.enabledAt(context.Background(), log.DebugLevel) {
		return
	}
	a.inner.Debug(fmt.Sprintf(format, args...))
}
func (a *adapter) DebugContext(ctx context.Context, args ...any) {
	msg, fields := goaktArgsToMsg(args)
	a.inner.DebugContext(ctx, msg, fields...)
}
func (a *adapter) DebugfContext(ctx context.Context, format string, args ...any) {
	if !a.enabledAt(ctx, log.DebugLevel) {
		return
	}
	a.inner.DebugContext(ctx, fmt.Sprintf(format, args...))
}
func (a *adapter) Info(args ...any) {
	msg, fields := goaktArgsToMsg(args)
	a.inner.Info(msg, fields...)
}
func (a *adapter) Infof(format string, args ...any) {
	if !a.enabledAt(context.Background(), log.InfoLevel) {
		return
	}
	a.inner.Info(fmt.Sprintf(format, args...))
}
func (a *adapter) InfoContext(ctx context.Context, args ...any) {
	msg, fields := goaktArgsToMsg(args)
	a.inner.InfoContext(ctx, msg, fields...)
}
func (a *adapter) InfofContext(ctx context.Context, format string, args ...any) {
	if !a.enabledAt(ctx, log.InfoLevel) {
		return
	}
	a.inner.InfoContext(ctx, fmt.Sprintf(format, args...))
}
func (a *adapter) Warn(args ...any) {
	msg, fields := goaktArgsToMsg(args)
	a.inner.Warn(msg, fields...)
}
func (a *adapter) Warnf(format string, args ...any) {
	if !a.enabledAt(context.Background(), log.WarningLevel) {
		return
	}
	a.inner.Warn(fmt.Sprintf(format, args...))
}
func (a *adapter) WarnContext(ctx context.Context, args ...any) {
	msg, fields := goaktArgsToMsg(args)
	a.inner.WarnContext(ctx, msg, fields...)
}
func (a *adapter) WarnfContext(ctx context.Context, format string, args ...any) {
	if !a.enabledAt(ctx, log.WarningLevel) {
		return
	}
	a.inner.WarnContext(ctx, fmt.Sprintf(format, args...))
}
func (a *adapter) Error(args ...any) {
	msg, fields := goaktArgsToMsg(args)
	a.inner.Error(msg, fields...)
}
func (a *adapter) Errorf(format string, args ...any) {
	if !a.enabledAt(context.Background(), log.ErrorLevel) {
		return
	}
	a.inner.Error(fmt.Sprintf(format, args...))
}
func (a *adapter) ErrorContext(ctx context.Context, args ...any) {
	msg, fields := goaktArgsToMsg(args)
	a.inner.ErrorContext(ctx, msg, fields...)
}
func (a *adapter) ErrorfContext(ctx context.Context, format string, args ...any) {
	if !a.enabledAt(ctx, log.ErrorLevel) {
		return
	}
	a.inner.ErrorContext(ctx, fmt.Sprintf(format, args...))
}

// LogLevel reports the most verbose level the backend currently accepts. It
// is resolved per call, never cached, so a runtime SetLevel is honored. When
// the backend enables nothing — DiscardLogger, for instance — the result is
// InvalidLevel, matching what GoAkt's own adapters report for an unmappable
// level.
func (a *adapter) LogLevel() log.Level {
	for _, l := range goaktLevelsByVerbosity {
		if a.enabledAt(context.Background(), l) {
			return l
		}
	}
	return log.InvalidLevel
}

// Enabled reports whether the backend accepts the given level. GoAkt's
// Logger interface carries no context here, so Background is used; the
// *Context methods forward the caller's real context instead.
func (a *adapter) Enabled(l log.Level) bool {
	return a.enabledAt(context.Background(), l)
}

// With returns a child logger carrying the given key-value pairs. The child is
// built by kit-logger itself, so the fields live in the backend and chained
// calls accumulate there; the adapter keeps no field slice of its own.
func (a *adapter) With(keyValues ...any) log.Logger {
	if len(keyValues) == 0 {
		return a
	}
	return newAdapter(a.backend.With(keyValues...))
}

// Flush delivers every record the backend has accepted so far. A buffered
// kit-logger drains asynchronously, so GoAkt's flush at shutdown is forwarded
// to the backend's own lifecycle when it exposes one; the drain is bounded by
// kit-logger's DefaultShutdownTimeout so a stuck downstream handler cannot
// hang the actor system's stop. The logger itself is never shut down: the
// application that built it owns its lifecycle.
func (a *adapter) Flush() error {
	if managed, ok := a.backend.(kitlog.ManagedLogger); ok {
		ctx, cancel := context.WithTimeout(context.Background(), kitlog.DefaultShutdownTimeout)
		defer cancel()
		return managed.Flush(ctx)
	}
	return a.backend.Sync()
}

// StdLogger returns a standard library logger whose every line is written as
// an INFO record through the backend, so third-party code that only accepts a
// *log.Logger still lands in the same output as everything else.
func (a *adapter) StdLogger() *golog.Logger {
	return golog.New(&loggerWriter{inner: a.backend}, "", 0)
}

// loggerWriter adapts Logger.Info to io.Writer for use with *log.Logger.
type loggerWriter struct {
	inner kitlog.Logger
}

func (w *loggerWriter) Write(p []byte) (int, error) {
	w.inner.Info(strings.TrimRight(string(p), "\r\n"))
	return len(p), nil
}
