// Command render is the project-archetype renderer. It validates an input
// file, renders the intended project files and applies them to a destination
// under a never-overwrite collision policy.
//
// Usage:
//
//	render validate --skill DIR --input FILE
//	render plan     --skill DIR --input FILE --dest DIR [--report FILE]
//	render render   --skill DIR --input FILE --dest DIR [--report FILE]
//
// Exit codes: 0 success; 1 internal error; 2 invalid input or unsafe
// destination (nothing written); 3 rendered with unresolved conflicts.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	exitOK        = 0
	exitInternal  = 1
	exitInvalid   = 2
	exitConflicts = 3
)

// Report is the optional machine-readable record of one invocation. It is
// only ever written outside the destination.
type Report struct {
	Command   string   `json:"command"`
	OutputID  string   `json:"output_id"`
	Actions   []Action `json:"actions"`
	Conflicts []string `json:"conflicts"`
	ExitCode  int      `json:"exit_code"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: render <validate|plan|render> --skill DIR --input FILE [--dest DIR] [--report FILE]")
		return exitInvalid
	}
	cmd := args[0]
	fsFlags := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fsFlags.SetOutput(stderr)
	skill := fsFlags.String("skill", "", "skill package directory")
	input := fsFlags.String("input", "", "input JSON file")
	dest := fsFlags.String("dest", "", "destination directory")
	report := fsFlags.String("report", "", "optional report path outside the destination")
	if err := fsFlags.Parse(args[1:]); err != nil {
		return exitInvalid
	}
	switch cmd {
	case "validate", "plan", "render":
	default:
		fmt.Fprintf(stderr, "error: unknown command %q\n", cmd)
		return exitInvalid
	}

	var problems []string
	if *skill == "" {
		problems = append(problems, "--skill is required")
	}
	if *input == "" {
		problems = append(problems, "--input is required")
	}
	if cmd != "validate" {
		problems = append(problems, destinationProblems(*dest, *skill, *report)...)
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintf(stderr, "error: %s\n", p)
		}
		return exitInvalid
	}

	raw, err := os.ReadFile(*input)
	if err != nil {
		fmt.Fprintf(stderr, "error: cannot read input: %v\n", err)
		return exitInvalid
	}
	cfg, err := ParseInput(raw)
	if err != nil {
		var verrs ValidationErrors
		if errors.As(err, &verrs) {
			for _, e := range verrs {
				fmt.Fprintf(stderr, "error: %s\n", e)
			}
		} else {
			fmt.Fprintf(stderr, "error: %v\n", err)
		}
		return exitInvalid
	}

	targets, err := BuildTargets(*skill, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitInternal
	}
	id, err := OutputID(cfg, targets)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitInternal
	}
	if cmd == "validate" {
		// The resolved, non-secret configuration (defaults applied), so the
		// user can confirm derived values before anything is written.
		resolved, err := json.Marshal(cfg)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitInternal
		}
		fmt.Fprintf(stdout, "resolved\t%s\n", resolved)
		fmt.Fprintf(stdout, "output_id\t%s\n", id)
		return exitOK
	}

	manifest, err := BuildManifest(cfg, id, targets)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitInternal
	}
	plan, err := PlanDestination(*dest, id, targets, manifest)
	if err != nil {
		var refusals RefusalErrors
		if errors.As(err, &refusals) {
			for _, r := range refusals {
				fmt.Fprintf(stderr, "error: %s\n", r)
			}
			return exitInvalid
		}
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitInternal
	}
	if cmd == "render" {
		if err := Apply(*dest, plan); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitInternal
		}
	}

	code := exitOK
	if len(plan.Conflicts) > 0 {
		code = exitConflicts
	}
	for _, a := range plan.Actions {
		fmt.Fprintf(stdout, "%s\t%s\n", a.Action, a.Path)
	}
	fmt.Fprintf(stdout, "output_id\t%s\n", id)
	if code == exitConflicts {
		fmt.Fprintf(stdout, "result\tconflicts (%d); proposals are under %s/proposed/; reconcile manually\n", len(plan.Conflicts), OutputDir(id))
	} else {
		fmt.Fprintf(stdout, "result\tok\n")
	}

	if *report != "" {
		rep := Report{Command: cmd, OutputID: id, Actions: plan.Actions, Conflicts: plan.Conflicts, ExitCode: code}
		if rep.Conflicts == nil {
			rep.Conflicts = []string{}
		}
		b, err := json.MarshalIndent(rep, "", "  ")
		if err == nil {
			err = writeReport(*report, append(b, '\n'))
		}
		if err != nil {
			fmt.Fprintf(stderr, "error: cannot write report: %v\n", err)
			return exitInternal
		}
	}
	return code
}

// destinationProblems checks the destination and report location before any
// input processing. Host-level checks (filesystem root, the Skill directory)
// are repeated by render.sh, which sees host paths.
func destinationProblems(dest, skill, report string) []string {
	var problems []string
	if dest == "" {
		return append(problems, "--dest is required")
	}
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return append(problems, fmt.Sprintf("cannot resolve destination: %v", err))
	}
	info, err := os.Lstat(absDest)
	switch {
	case err != nil:
		problems = append(problems, fmt.Sprintf("destination %s does not exist", dest))
	case info.Mode()&os.ModeSymlink != 0:
		problems = append(problems, "destination must not be a symlink")
	case !info.IsDir():
		problems = append(problems, "destination is not a directory")
	}
	if absDest == "/" {
		problems = append(problems, "destination must not be the filesystem root")
	}
	if skill != "" {
		if absSkill, err := filepath.Abs(skill); err == nil && within(absDest, absSkill) {
			problems = append(problems, "destination must not be the Skill package or inside it")
		}
	}
	if report != "" {
		if absReport, err := filepath.Abs(report); err == nil && within(absReport, absDest) {
			problems = append(problems, "--report must be outside the destination")
		}
	}
	return problems
}

// writeReport writes the report without following a symlink at its path.
func writeReport(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// within reports whether p is base or inside base.
func within(p, base string) bool {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, "../"))
}
