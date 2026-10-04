package main

import (
	"fmt"
	"sort"
)

// CheckGate compares the result of every job ci-ok waits for with the plan. needs maps a job id to its result
// (success, failure, cancelled or skipped) and lanes maps it to whether the plan required it to run. It returns
// one message per problem; no messages means the run is acceptable.
//
// A job the plan did not require may be skipped, and may even have run. A job the plan required must have
// succeeded: skipped is not enough, because that is how a broken condition would turn a missing lane green.
func CheckGate(needs map[string]string, lanes map[string]bool) []string {
	var problems []string

	if got := needs["plan"]; got != "success" {
		problems = append(problems, fmt.Sprintf("plan: result is %q, the plan is what the other jobs are checked against", got))
	}
	for _, name := range sortedKeys(needs) {
		result := needs[name]
		if result != "success" && result != "skipped" {
			problems = append(problems, fmt.Sprintf("%s: result is %q", name, result))
		}
	}
	for _, name := range allLanes {
		run, planned := lanes[name]
		result, present := needs[name]
		switch {
		case !planned:
			problems = append(problems, fmt.Sprintf("%s: the plan has no entry for this job", name))
		case !present:
			problems = append(problems, fmt.Sprintf("%s: the plan lists it but ci-ok does not wait for it", name))
		case run && result != "success":
			problems = append(problems, fmt.Sprintf("%s: required by the plan but its result is %q", name, result))
		}
	}
	for _, name := range sortedKeys(needs) {
		if name == "plan" || contains(allLanes, name) {
			continue
		}
		problems = append(problems, fmt.Sprintf("%s: ci-ok waits for this job but the planner does not know it", name))
	}
	return problems
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, v string) bool {
	for _, e := range list {
		if e == v {
			return true
		}
	}
	return false
}
