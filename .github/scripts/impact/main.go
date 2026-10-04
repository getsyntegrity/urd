package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const usage = `usage:
  impact plan -event E -base-ref B -head-ref H [-changes FILE] [-root DIR] [-plan-out F] [-summary-out F] [-packages-out F] [-github-output F]
  impact gate   (reads the NEEDS and LANES environment variables)
  impact architecture [-out DIR]   (runs the architecture plan of the ARCHITECTURE environment variable)`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "plan":
		err = runPlan(os.Args[2:], os.Stdout)
	case "gate":
		err = runGate(os.Getenv("NEEDS"), os.Getenv("LANES"), os.Stdout)
	case "architecture":
		err = runArchitecture(os.Args[2:], os.Getenv("ARCHITECTURE"), os.Stdout, os.Stderr, execGoTest, execGoVersion)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "impact:", err)
		os.Exit(1)
	}
}

func runPlan(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	root := fs.String("root", ".", "repository root")
	event := fs.String("event", "", "GitHub event name: pull_request, push or workflow_dispatch")
	baseRef := fs.String("base-ref", "", "base branch of the pull request")
	headRef := fs.String("head-ref", "", "head branch of the pull request")
	changes := fs.String("changes", "", "file with the output of `git diff --name-status -M -z`; required unless the event validates everything")
	planOut := fs.String("plan-out", "", "write the plan as JSON")
	summaryOut := fs.String("summary-out", "", "write the readable plan (Markdown)")
	packagesOut := fs.String("packages-out", "", "write the selected root packages, one import path per line")
	githubOutput := fs.String("github-output", "", "append the workflow outputs (lanes, modules, race_packages, architecture) to this file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ev := Event{Name: *event, BaseRef: *baseRef, HeadRef: *headRef}
	graph, err := LoadGraph(os.DirFS(*root))
	if err != nil {
		return err
	}
	var list []Change
	if !ev.fullByEvent() {
		if *changes == "" {
			return fmt.Errorf("-changes is required for a %s event: without the diff nothing can be selected", ev.Kind())
		}
		raw, err := os.ReadFile(*changes)
		if err != nil {
			return err
		}
		if list, err = ParseChanges(string(raw)); err != nil {
			return err
		}
	}
	plan, err := BuildPlan(ev, graph, list)
	if err != nil {
		return err
	}

	if *planOut != "" {
		b, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*planOut, append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	if *summaryOut != "" {
		if err := os.WriteFile(*summaryOut, []byte(plan.Summary()), 0o644); err != nil {
			return err
		}
	}
	// Only a selective plan writes the list: its absence is how test-matrix.sh knows to plan every package.
	if list := plan.SelectedPackagesFile(); *packagesOut != "" && list != "" {
		if err := os.WriteFile(*packagesOut, []byte(list+"\n"), 0o644); err != nil {
			return err
		}
	}
	if *githubOutput != "" {
		if err := appendOutputs(*githubOutput, plan); err != nil {
			return err
		}
	}
	_, err = io.WriteString(out, plan.Summary())
	return err
}

func appendOutputs(file string, plan *Plan) error {
	lanes, err := plan.LanesJSON()
	if err != nil {
		return err
	}
	modules, err := plan.ModulesMatrix()
	if err != nil {
		return err
	}
	architecture, err := plan.ArchitectureJSON()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "lanes=%s\nmodules=%s\nrace_packages=%s\narchitecture=%s\n", lanes, modules, plan.RacePackages(), architecture)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// runGate checks the job results that ci-ok received against the plan. needsJSON is toJSON(needs) and lanesJSON is
// the lanes output of the plan job.
func runGate(needsJSON, lanesJSON string, out io.Writer) error {
	var raw map[string]struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(needsJSON), &raw); err != nil {
		return fmt.Errorf("NEEDS is not the needs context: %w", err)
	}
	needs := map[string]string{}
	for name, n := range raw {
		needs[name] = n.Result
		fmt.Fprintf(out, "%s: %s\n", name, n.Result)
	}
	var lanes map[string]bool
	if strings.TrimSpace(lanesJSON) != "" {
		if err := json.Unmarshal([]byte(lanesJSON), &lanes); err != nil {
			return fmt.Errorf("LANES is not a lane map: %w", err)
		}
	}
	problems := CheckGate(needs, lanes)
	for _, p := range problems {
		fmt.Fprintln(out, "::error::"+p)
	}
	if len(problems) > 0 {
		return fmt.Errorf("%d problem(s): the run does not match the plan", len(problems))
	}
	fmt.Fprintln(out, "gate: every job the plan requires succeeded")
	return nil
}
