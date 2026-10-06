// Package measure holds the pure helpers of the #332 journal-cursor comparison: percentiles over duration samples
// and the stable order in which a multi-shard write takes its shard locks. It touches no database and no clock, so
// it is covered by plain unit tests.
package measure

import (
	"cmp"
	"slices"
	"time"
)

// Percentile returns the nearest-rank percentile (0 < p <= 100) of samples, or 0 when there are none. The input is
// not modified.
func Percentile(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	rank := int(p/100*float64(len(sorted)) + 0.999999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// Summary is the distribution the report prints for one series of samples.
type Summary struct {
	N             int
	P50, P95, P99 time.Duration
}

// Summarize computes the Summary of samples.
func Summarize(samples []time.Duration) Summary {
	return Summary{
		N:   len(samples),
		P50: Percentile(samples, 50),
		P95: Percentile(samples, 95),
		P99: Percentile(samples, 99),
	}
}

// SortedUnique returns the distinct values in ascending order. A write that spans several shards takes its shard
// locks in this order: two writers that share shards then always acquire them in the same order and cannot
// deadlock each other.
func SortedUnique[T cmp.Ordered](values []T) []T {
	return SortedUniqueFunc(values, cmp.Compare[T])
}

// SortedUniqueFunc is SortedUnique for a key that is not cmp.Ordered, such as a (tenant, shard) pair.
func SortedUniqueFunc[T any](values []T, compare func(a, b T) int) []T {
	out := slices.Clone(values)
	slices.SortFunc(out, compare)
	return slices.CompactFunc(out, func(a, b T) bool { return compare(a, b) == 0 })
}
