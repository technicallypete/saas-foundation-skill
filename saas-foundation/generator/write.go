package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

// Invocation actions.
const (
	ActionCreate      = "create"
	ActionUnchanged   = "unchanged"
	ActionConflict    = "conflict"
	ActionEnsureLines = "ensure-lines"
)

// Action records what one invocation does (or would do) to one path.
type Action struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Role   string `json:"role"` // output, manifest, proposal
}

type pendingWrite struct {
	path    string
	content []byte
	exec    bool
	append  bool
}

// Plan is the set of actions and writes for one invocation.
type Plan struct {
	Actions   []Action
	Conflicts []string
	writes    []pendingWrite
}

// RefusalErrors are destination-safety problems; nothing is written.
type RefusalErrors []string

func (r RefusalErrors) Error() string { return strings.Join(r, "; ") }

// PlanDestination compares intended targets with the destination without
// writing anything.
func PlanDestination(dest, id string, targets []Target, manifest Target) (Plan, error) {
	var plan Plan
	var refusals RefusalErrors

	all := make([]Target, 0, len(targets))
	all = append(all, targets...)

	decide := func(t Target, role string) (string, bool) {
		if msg := checkPathSafety(dest, t.Path); msg != "" {
			refusals = append(refusals, msg)
			return "", false
		}
		full := filepath.Join(dest, filepath.FromSlash(t.Path))
		existing, err := os.ReadFile(full)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			plan.writes = append(plan.writes, pendingWrite{path: t.Path, content: t.Content, exec: t.Exec})
			return ActionCreate, true
		case err != nil:
			refusals = append(refusals, fmt.Sprintf("cannot read %s: %v", t.Path, err))
			return "", false
		}
		if t.Mode == ModeEnsureLines {
			missing := missingLines(existing, t.Lines)
			if len(missing) == 0 {
				return ActionUnchanged, true
			}
			add := []byte(ensureBlock(missing))
			if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
				add = append([]byte("\n"), add...)
			}
			plan.writes = append(plan.writes, pendingWrite{path: t.Path, content: add, append: true})
			return ActionEnsureLines, true
		}
		if bytes.Equal(existing, t.Content) {
			return ActionUnchanged, true
		}
		return ActionConflict, true
	}

	for _, t := range all {
		action, ok := decide(t, "output")
		if !ok {
			continue
		}
		plan.Actions = append(plan.Actions, Action{Path: t.Path, Action: action, Role: "output"})
		if action == ActionConflict {
			plan.Conflicts = append(plan.Conflicts, t.Path)
			proposal := Target{Path: OutputDir(id) + "/proposed/" + t.Path, Content: t.Content, Mode: ModeFile, Exec: t.Exec}
			pa, ok := decide(proposal, "proposal")
			if !ok {
				continue
			}
			plan.Actions = append(plan.Actions, Action{Path: proposal.Path, Action: pa, Role: "proposal"})
			if pa == ActionConflict {
				plan.Conflicts = append(plan.Conflicts, proposal.Path)
			}
		}
	}
	if ma, ok := decide(manifest, "manifest"); ok {
		plan.Actions = append(plan.Actions, Action{Path: manifest.Path, Action: ma, Role: "manifest"})
		if ma == ActionConflict {
			plan.Conflicts = append(plan.Conflicts, manifest.Path)
		}
	}
	if len(refusals) > 0 {
		return Plan{}, refusals
	}
	return plan, nil
}

func missingLines(existing []byte, required []string) []string {
	have := map[string]bool{}
	for _, l := range strings.Split(string(existing), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var missing []string
	for _, r := range required {
		if !have[r] {
			missing = append(missing, r)
		}
	}
	return missing
}

// checkPathSafety refuses any target whose path escapes the destination, or
// whose existing parent or target is a symlink or of the wrong kind.
func checkPathSafety(dest, rel string) string {
	if err := checkRelative(rel); err != nil {
		return err.Error()
	}
	segs := strings.Split(rel, "/")
	cur := dest
	for i, seg := range segs {
		cur = filepath.Join(cur, seg)
		info, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return ""
		}
		if err != nil {
			return fmt.Sprintf("cannot inspect %s: %v", path.Join(segs[:i+1]...), err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Sprintf("refusing to write through symlink %s", path.Join(segs[:i+1]...))
		}
		last := i == len(segs)-1
		if !last && !info.IsDir() {
			return fmt.Sprintf("refusing: %s exists and is not a directory", path.Join(segs[:i+1]...))
		}
		if last && !info.Mode().IsRegular() {
			return fmt.Sprintf("refusing: %s exists and is not a regular file", rel)
		}
	}
	return ""
}

// Apply performs the planned writes. Creates never follow or replace an
// existing file; appends never follow symlinks.
func Apply(dest string, plan Plan) error {
	for _, w := range plan.writes {
		full := filepath.Join(dest, filepath.FromSlash(w.path))
		if msg := checkPathSafety(dest, w.path); msg != "" {
			return errors.New(msg)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		var f *os.File
		var err error
		if w.append {
			f, err = os.OpenFile(full, os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0)
		} else {
			mode := os.FileMode(0o644)
			if w.exec {
				mode = 0o755
			}
			f, err = os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
		}
		if err != nil {
			return fmt.Errorf("write %s: %w", w.path, err)
		}
		if _, err := f.Write(w.content); err != nil {
			f.Close()
			return fmt.Errorf("write %s: %w", w.path, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close %s: %w", w.path, err)
		}
	}
	return nil
}
