package measure_test

import (
	"testing"
	"time"

	"github.com/getsyntegrity/go-specs/specs"

	"github.com/getsyntegrity/urd/inttest/flows/eventstore/internal/measure"
)

func ms(n ...int) []time.Duration {
	out := make([]time.Duration, len(n))
	for i, v := range n {
		out[i] = time.Duration(v) * time.Millisecond
	}
	return out
}

func TestPercentile(t *testing.T) {
	specs.Describe(t, "measure.Percentile", func(s *specs.Spec) {
		s.It("uses the nearest rank", func(sc *specs.Context) {
			samples := ms(10, 20, 30, 40, 50, 60, 70, 80, 90, 100)
			sc.Expect(measure.Percentile(samples, 50)).To(specs.Equal(50 * time.Millisecond))
			sc.Expect(measure.Percentile(samples, 95)).To(specs.Equal(100 * time.Millisecond))
			sc.Expect(measure.Percentile(samples, 99)).To(specs.Equal(100 * time.Millisecond))
			sc.Expect(measure.Percentile(samples, 10)).To(specs.Equal(10 * time.Millisecond))
		})
		s.It("does not depend on the input order and does not modify it", func(sc *specs.Context) {
			samples := ms(30, 10, 20)
			sc.Expect(measure.Percentile(samples, 50)).To(specs.Equal(20 * time.Millisecond))
			sc.Expect(samples).To(specs.Equal(ms(30, 10, 20)))
		})
		s.It("returns zero without samples", func(sc *specs.Context) {
			sc.Expect(measure.Percentile(nil, 99)).To(specs.Equal(time.Duration(0)))
			sc.Expect(measure.Summarize(nil).N).To(specs.Equal(0))
		})
		s.It("summarizes one sample as itself", func(sc *specs.Context) {
			got := measure.Summarize(ms(7))
			sc.Expect(got).To(specs.Equal(measure.Summary{N: 1, P50: 7 * time.Millisecond, P95: 7 * time.Millisecond, P99: 7 * time.Millisecond}))
		})
	})
}

func TestSortedUnique(t *testing.T) {
	specs.Describe(t, "measure.SortedUnique", func(s *specs.Spec) {
		s.It("orders and de-duplicates the shards of a multi-shard write", func(sc *specs.Context) {
			sc.Expect(measure.SortedUnique([]int64{5, 1, 5, 3, 1})).To(specs.Equal([]int64{1, 3, 5}))
		})
		s.It("gives writers that list the same shards in opposite order the same lock order", func(sc *specs.Context) {
			sc.Expect(measure.SortedUnique([]int64{2, 1})).To(specs.Equal(measure.SortedUnique([]int64{1, 2})))
		})
		s.It("does not modify its input", func(sc *specs.Context) {
			in := []int64{3, 1}
			_ = measure.SortedUnique(in)
			sc.Expect(in).To(specs.Equal([]int64{3, 1}))
		})
		s.It("orders a (scope, shard) pair key with a comparison function", func(sc *specs.Context) {
			type key struct {
				tenant string
				shard  int64
			}
			cmpKey := func(a, b key) int {
				if a.tenant != b.tenant {
					return map[bool]int{true: -1, false: 1}[a.tenant < b.tenant]
				}
				return int(a.shard - b.shard)
			}
			got := measure.SortedUniqueFunc([]key{{"b", 1}, {"a", 2}, {"a", 1}, {"a", 2}}, cmpKey)
			sc.Expect(got).To(specs.Equal([]key{{"a", 1}, {"a", 2}, {"b", 1}}))
		})
		s.It("returns nothing for nothing", func(sc *specs.Context) {
			sc.Expect(len(measure.SortedUnique[int64](nil))).To(specs.Equal(0))
		})
	})
}
