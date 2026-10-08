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

package readertck

import (
	"fmt"
	"slices"
)

// Property names one of the three properties of a Verdict.
type Property uint8

const (
	OnSafety Property = iota + 1
	OnEligibility
	OnProgress
)

// Of returns the verdict of p.
func (v Verdict) Of(p Property) PropertyVerdict {
	switch p {
	case OnSafety:
		return v.Safety
	case OnEligibility:
		return v.Eligibility
	default:
		return v.Progress
	}
}

// Expectation is the exact outcome of one property: its status and the distinct
// violation codes it carries.
type Expectation struct {
	Status Status
	Codes  []string
}

func distinctCodes(p PropertyVerdict) []string {
	var codes []string
	for _, v := range p.Violations {
		if !slices.Contains(codes, v.Code) {
			codes = append(codes, v.Code)
		}
	}
	slices.Sort(codes)
	return codes
}

// Deviations lists every way v differs from a declared baseline row, the exact
// outcome that a known failure is EXPECTED to have, measured by running it. A
// property the row declares must have exactly that status and set of codes. A
// property the row does not mention must not fail and must carry no violation,
// so an additional code or an additional failing property is a deviation and is
// never absorbed. A nil row means the verdict must be entirely clean.
func Deviations(v Verdict, row map[Property]Expectation) []string {
	var out []string
	for _, p := range []Property{OnSafety, OnEligibility, OnProgress} {
		got := v.Of(p)
		want, declared := row[p]
		if !declared {
			if got.Status == Fail || len(got.Violations) > 0 {
				out = append(out, fmt.Sprintf("property %d: unexpected %v %v", p, got.Status, distinctCodes(got)))
			}
			continue
		}
		wantCodes := slices.Clone(want.Codes)
		slices.Sort(wantCodes)
		if got.Status != want.Status || !slices.Equal(distinctCodes(got), wantCodes) {
			out = append(out, fmt.Sprintf("property %d: got %v %v, want %v %v", p, got.Status, distinctCodes(got), want.Status, wantCodes))
		}
	}
	return out
}
