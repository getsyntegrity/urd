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

package postgres

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
)

// ErrUnsupportedSchema is returned by Migrate when the database holds a schema
// that this module cannot upgrade by itself: an events_store from before the
// scoped stores, which has no tenant_id column. Its primary key has to change
// first, by hand, with the ALTER recipe of example/cluster/README.md.
var ErrUnsupportedSchema = errors.New("postgres: unsupported legacy schema")

// ErrSchemaAhead is returned by Migrate when the database records a version
// newer than the last schema file this binary embeds: a newer build already
// migrated it. Running on regardless could write against tables this build does
// not understand, so Migrate fails and Engine.Start with it.
var ErrSchemaAhead = errors.New("postgres: schema is newer than this binary")

// ErrNotConnected is returned by Migrate and SchemaVersion on a store whose
// Connect has not succeeded yet.
var ErrNotConnected = errors.New("postgres: store is not connected")

// schemaDir is the directory of schemaFS that holds the numbered SQL files.
const schemaDir = "schema"

// schemaFS holds the versioned SQL files, embedded so the module ships its own
// schema. A file is named <version>_<what it does>.sql, with versions that run
// 1, 2, 3 without gaps. Once released, a file is never edited: a change is a
// new file.
//
//go:embed schema/*.sql
var schemaFS embed.FS

// schemaFile is one versioned SQL file.
type schemaFile struct {
	version uint
	name    string
	sql     string
}

// loadSchemaFiles reads the numbered .sql files of dir, ordered by version. It
// refuses a set that could be applied wrongly: a name without a numeric prefix,
// version 0 (which means "never migrated"), a duplicate or a gap in the
// versions, an empty file, or no file at all. Files that do not end in .sql
// are ignored.
func loadSchemaFiles(fsys fs.FS, dir string) ([]schemaFile, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("postgres: read schema files: %w", err)
	}

	byVersion := make(map[uint]schemaFile, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, found := strings.Cut(entry.Name(), "_")
		number, convErr := strconv.ParseUint(prefix, 10, 32)
		if !found || convErr != nil {
			return nil, fmt.Errorf("postgres: schema file %q is not named <version>_<name>.sql", entry.Name())
		}
		version := uint(number)
		if version == 0 {
			return nil, fmt.Errorf("postgres: schema file %q has version 0, which is reserved for a database that was never migrated", entry.Name())
		}
		if previous, dup := byVersion[version]; dup {
			return nil, fmt.Errorf("postgres: schema files %q and %q have the duplicate version %d", previous.name, entry.Name(), version)
		}
		content, readErr := fs.ReadFile(fsys, path.Join(dir, entry.Name()))
		if readErr != nil {
			return nil, fmt.Errorf("postgres: read schema file %q: %w", entry.Name(), readErr)
		}
		if strings.TrimSpace(string(content)) == "" {
			return nil, fmt.Errorf("postgres: schema file %q is empty", entry.Name())
		}
		byVersion[version] = schemaFile{version: version, name: entry.Name(), sql: string(content)}
	}

	if len(byVersion) == 0 {
		return nil, fmt.Errorf("postgres: no schema files in %q", dir)
	}
	files := make([]schemaFile, 0, len(byVersion))
	for version := uint(1); version <= uint(len(byVersion)); version++ {
		file, ok := byVersion[version]
		if !ok {
			return nil, fmt.Errorf("postgres: schema files have a gap: version %d is missing", version)
		}
		files = append(files, file)
	}
	return files, nil
}

// pendingSchemaFiles returns the files whose version is above current, in order.
func pendingSchemaFiles(files []schemaFile, current uint) []schemaFile {
	for i, f := range files {
		if f.version > current {
			return files[i:]
		}
	}
	return nil
}

// checkSchemaNotAhead returns ErrSchemaAhead, wrapped with both versions, when
// the database's current version is above latest, the version of the newest
// embedded file.
func checkSchemaNotAhead(current, latest uint) error {
	if current > latest {
		return fmt.Errorf("postgres: schema: database is at version %d, this binary knows up to %d: %w", current, latest, ErrSchemaAhead)
	}
	return nil
}

// catalog is the set of objects found in a database, as the keys
// "<table>", "<table>.<column>" and "index.<index name>". It is how the pure
// baseline logic sees a database without a connection.
type catalog map[string]bool

// baselineMarkers lists, in the order of the files, the object whose presence
// shows that file's version was already applied to a database created before
// versions were recorded. Entry i belongs to version i+1. Every marker is
// something its own file creates, so the file and its marker move together; a
// test asserts there is one marker per file.
var baselineMarkers = []string{
	"events_store.tenant_id",       // 001_events_store
	"index.idx_events_store_shard", // 002_events_store_indexes
	"events_store_revisions",       // 003_events_store_revisions
	"events_store.tenant_metadata", // 004_events_store_tenant_metadata
	"offsets_store",                // 005_offsets_store
	"offsets_store.tenant_id",      // 006_scoped_offsets
}

// inferSchemaVersion tells which version a database that holds no version
// record is at, from the objects it already has. The answer is the longest run
// of markers, from version 1, that are all present: a later object without an
// earlier one does not skip a version, so what is missing is applied again.
// That is safe because every file is idempotent; the point of the inference is
// not to re-run the expensive ones, such as the backfill of 003, on a database
// that already went through them.
//
// An events_store with no tenant_id column predates the scoped stores and is
// refused with ErrUnsupportedSchema instead of being guessed at.
func inferSchemaVersion(found catalog) (uint, error) {
	if found["events_store"] && !found[baselineMarkers[0]] {
		return 0, fmt.Errorf("%w: events_store has no tenant_id column; add it first, by hand, with the ALTER recipe in example/cluster/README.md", ErrUnsupportedSchema)
	}
	var version uint
	for _, marker := range baselineMarkers {
		if !found[marker] {
			break
		}
		version++
	}
	return version, nil
}
