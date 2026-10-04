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

package engine

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/getsyntegrity/go-specs/specs"
)

// loggerSeamFile is the one first-party file allowed to speak GoAkt's logging
// API: it defines the adapter that presents kit-logger to the actor system.
// The path is relative to the module root, where the scan starts.
const loggerSeamFile = "internal/goaktlog/adapter.go"

// bannedLoggerConstructs are logging constructs first-party production code
// must not contain anywhere. Urd logs through kit-logger, so no default may be
// derived from a concrete third-party backend the caller cannot replace.
var bannedLoggerConstructs = []string{
	"log.NewZap(",
	"go.uber.org/zap",
	"log.DefaultLogger",
	"log.DiscardLogger",
}

// bannedOutsideLoggerSeam are constructs allowed only in the seam file.
// Anywhere else they are a parallel logging backend: a record that bypasses
// the kit-logger Logger the application configured, its level and its fields.
//
// The scan is a substring match, so every entry is written the way it appears
// in source.
var bannedOutsideLoggerSeam = []string{
	`"github.com/tochemey/goakt/v4/log"`,
	"\t\"log\"\n",
	"slog.New(",
	"slog.Default(",
	"slog.SetDefault(",
	"slog.Debug(",
	"slog.Info(",
	"slog.Warn(",
	"slog.Error(",
	"log.Printf(",
	"log.Println(",
	"log.Fatal",
	"fmt.Printf(",
	"fmt.Println(",
}

// loggerArchitectureSkippedDirs are directories excluded from the scan.
//
//   - vendor: third-party sources, not ours to constrain.
//   - example, benchmark: standalone modules that are free to pick any logger.
//   - .git, .idea, .codegraph, .atl, openspec: tooling metadata, not Go sources.
var loggerArchitectureSkippedDirs = map[string]struct{}{
	"vendor":     {},
	"example":    {},
	"benchmark":  {},
	".git":       {},
	".idea":      {},
	".codegraph": {},
	".atl":       {},
	"openspec":   {},
}

// isScannedGoSource reports whether a path is first-party production Go code.
// Test files carry their own fixtures and generated protobuf code is not
// hand-written, so neither is subject to this rule.
func isScannedGoSource(path string) bool {
	switch {
	case !strings.HasSuffix(path, ".go"):
		return false
	case strings.HasSuffix(path, "_test.go"):
		return false
	case strings.HasSuffix(path, ".pb.go"):
		return false
	default:
		return true
	}
}

// TestArchitectureKitLoggerIsTheOnlyLoggingBackend guards the logging boundary. No
// first-party production file may construct a concrete third-party logger, and
// none but the seam file may reach for GoAkt's logging API or the standard
// library's, because the only supported way to log is through the kit-logger
// Logger the application passes to WithLogger.
func TestArchitectureKitLoggerIsTheOnlyLoggingBackend(t *testing.T) {
	specs.Describe(t, "first-party production code logs only through kit-logger", func(s *specs.Spec) {
		s.It("has no concrete logger and no parallel logging backend outside the seam", func(ctx *specs.Context) {
			root := architectureModuleRoot(ctx.T)

			// Violations are collected as "path must not use construct" lines, so
			// a failure names every offending path and construct instead of
			// dumping a source file.
			var scanned int
			var violations []string
			walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}

				if entry.IsDir() {
					if _, skipped := loggerArchitectureSkippedDirs[entry.Name()]; skipped {
						return filepath.SkipDir
					}
					return nil
				}

				if !isScannedGoSource(path) {
					return nil
				}

				content, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				scanned++

				rel, err := filepath.Rel(root, path)
				if err != nil {
					rel = path
				}
				for _, banned := range bannedLoggerConstructs {
					if strings.Contains(string(content), banned) {
						violations = append(violations,
							rel+" must not use "+strconv.Quote(banned)+": log through kit-logger instead")
					}
				}

				if rel == loggerSeamFile {
					return nil
				}
				for _, banned := range bannedOutsideLoggerSeam {
					if strings.Contains(string(content), banned) {
						violations = append(violations, rel+" must not use "+strconv.Quote(banned)+
							": log through the kit-logger Logger it was given, not a parallel backend")
					}
				}
				return nil
			})

			ctx.Expect(walkErr).To(specs.BeNil())
			ctx.Expect(violations).To(specs.BeEmpty())
			// the scan found Go sources, so it proves something
			ctx.Expect(scanned).To(specs.BeGreaterThan(0))
		})
	})
}
