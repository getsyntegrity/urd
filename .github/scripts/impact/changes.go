package main

import (
	"fmt"
	"strings"
)

// Change is one file a pull request touched, as `git diff --name-status` reports it.
type Change struct {
	Status string // A added, M modified, D deleted; a rename is split into a D and an A by ParseChanges
	Path   string
}

// ParseChanges reads the output of `git diff --name-status -M -z`: NUL-separated fields, a status letter then
// one path, or two for a rename or a copy. A rename becomes a delete of the old path and an add of the new one,
// because the selector treats a path that disappears differently from one that changes. An unknown or unmerged
// status is an error: guessing would hide a change.
func ParseChanges(raw string) ([]Change, error) {
	fields := strings.Split(raw, "\x00")
	// -z ends every record with NUL, so the last field is empty.
	if n := len(fields); n > 0 && fields[n-1] == "" {
		fields = fields[:n-1]
	}
	var out []Change
	for i := 0; i < len(fields); {
		status := fields[i]
		if status == "" {
			return nil, fmt.Errorf("changes: empty status at field %d", i)
		}
		switch status[0] {
		case 'A', 'M', 'D', 'T':
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("changes: status %q has no path", status)
			}
			kind := string(status[0])
			if kind == "T" {
				kind = "M"
			}
			out = append(out, Change{Status: kind, Path: fields[i+1]})
			i += 2
		case 'R', 'C':
			if i+2 >= len(fields) {
				return nil, fmt.Errorf("changes: status %q needs two paths", status)
			}
			if status[0] == 'R' {
				out = append(out, Change{Status: "D", Path: fields[i+1]})
			}
			out = append(out, Change{Status: "A", Path: fields[i+2]})
			i += 3
		default:
			return nil, fmt.Errorf("changes: unsupported status %q", status)
		}
	}
	return out, nil
}
