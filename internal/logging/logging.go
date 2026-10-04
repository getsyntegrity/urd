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

// Package logging holds the logger-resolution glue that both the GoAkt
// runtime adapter (package engine) and application-layer packages such as
// migration need: the default kit-logger instance and the typed-nil-safe
// fallback used whenever a caller may hand in a nil or typed-nil Logger.
//
// It exists so that a package outside the runtime adapter — migration, at
// this slice (#147, ego-arch-001 §3, S4-1) — can resolve a usable logger
// without importing package engine, whose dependency closure includes the
// GoAkt runtime. This package imports only kit-logger and the standard
// library, so its own closure carries neither; TestArchitectureLoggingStaysRuntimeNeutral
// guards that. The adapter that implements GoAkt's log.Logger lives in
// internal/goaktlog, which depends on this package and never the reverse.
//
// engine.DefaultLogger and engine.ResolveLogger keep their exact signatures and
// behavior in package engine; they delegate to DefaultLogger and ResolveLogger
// here rather than duplicating the logic, so there is exactly one
// typed-nil-detection implementation and one process-wide default.
package logging

import (
	"reflect"

	kitlog "github.com/pablogore/kit-logger/pkg/logger"
)

// DefaultLogger returns the Logger used when none is supplied: kit-logger's
// process-wide logger, as returned by logger.L(). An application that
// installs its own logger with logger.SetGlobal before building the engine
// or the migrator therefore gets every record through it without passing an
// explicit logger option at all.
//
// It is a function rather than a variable so that importing this package
// never constructs the global logger as a side effect; the lookup happens
// when a caller actually resolves a logger.
func DefaultLogger() kitlog.Logger {
	return kitlog.L()
}

// isNilLogger returns true when l is nil or a typed-nil (e.g. (*MyLogger)(nil)).
// A typed-nil interface value is non-nil at the interface level but wraps a nil
// pointer, which would cause a nil-dereference panic on the first log call.
func isNilLogger(l kitlog.Logger) bool {
	if l == nil {
		return true
	}
	v := reflect.ValueOf(l)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// ResolveLogger returns logger when it is usable, and DefaultLogger() when it
// is nil or a typed-nil pointer. It lets packages that take a caller-supplied
// Logger apply the same nil-logger semantics, without each of them
// re-implementing the typed-nil detection.
func ResolveLogger(logger kitlog.Logger) kitlog.Logger {
	if isNilLogger(logger) {
		return DefaultLogger()
	}
	return logger
}
