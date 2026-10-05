package main

// Skill package structure tests, run by the static suite:
//   TestSkillFrontmatter  (S-FRONTMATTER; frontmatter parsed by yq, see FRONTMATTER_JSON)
//   TestSkillLinks        (S-LINKS)
//   TestSkillPaths        (S-PATHS)
//   TestSkillTrace        (S-TRACE)

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func skillRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(skillDir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

var (
	skillNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	allowedKeys = map[string]bool{"name": true, "description": true, "license": true, "compatibility": true, "metadata": true, "allowed-tools": true}
	requiredH2  = []string{"When to use", "Prerequisites", "Essential invariants", "Inputs", "Workflow", "Output contract", "Verification", "References"}
)

func TestSkillFrontmatter(t *testing.T) {
	path := os.Getenv("FRONTMATTER_JSON")
	if path == "" {
		t.Skip("FRONTMATTER_JSON not set (the static suite parses frontmatter with yq)")
	}
	var fm map[string]any
	if err := json.Unmarshal(readFile(t, path), &fm); err != nil {
		t.Fatalf("frontmatter JSON: %v", err)
	}
	for k := range fm {
		if !allowedKeys[k] {
			t.Errorf("frontmatter key %q is not allowed by the Agent Skills specification", k)
		}
	}
	name, _ := fm["name"].(string)
	if len(name) < 1 || len(name) > 64 || !skillNameRe.MatchString(name) || name != filepath.Base(skillRoot(t)) {
		t.Errorf("name %q must be 1-64 [a-z0-9-] characters and equal the directory name", name)
	}
	desc, _ := fm["description"].(string)
	if strings.TrimSpace(desc) == "" || len(desc) > 1024 {
		t.Errorf("description must be 1-1024 characters (got %d)", len(desc))
	}
	if c, ok := fm["compatibility"].(string); ok && len(c) > 500 {
		t.Error("compatibility must be at most 500 characters")
	}
	meta, _ := fm["metadata"].(map[string]any)
	if v, _ := meta["version"].(string); v != SkillVersion {
		t.Errorf("metadata.version %q must equal the renderer SkillVersion %q", v, SkillVersion)
	}
	body := string(readFile(t, filepath.Join(skillRoot(t), "SKILL.md")))
	for _, h := range requiredH2 {
		if !regexp.MustCompile(`(?m)^## ` + regexp.QuoteMeta(h) + `\b`).MatchString(body) {
			t.Errorf("SKILL.md lacks section %q", h)
		}
	}
	if lines := strings.Count(body, "\n"); lines > 400 {
		t.Errorf("SKILL.md has %d lines; keep it under about 400 and move detail to references", lines)
	}
}

// markdownFiles returns Skill documentation files (templates are rendered
// into destinations and are checked there instead).
func markdownFiles(t *testing.T) []string {
	t.Helper()
	root := skillRoot(t)
	var out []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() && (rel == "templates" || strings.HasPrefix(rel, "validation/isolation")) {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, ".md") {
			out = append(out, p)
		}
		return nil
	})
	if len(out) == 0 {
		t.Fatal("no markdown files found")
	}
	return out
}

var (
	linkRe    = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	headingRe = regexp.MustCompile(`(?m)^#{1,6}\s+(.+?)\s*$`)
)

func slugify(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	var b strings.Builder
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}

func anchors(t *testing.T, file string) map[string]bool {
	out := map[string]bool{}
	for _, m := range headingRe.FindAllStringSubmatch(string(readFile(t, file)), -1) {
		out[slugify(strings.Trim(m[1], "`*"))] = true
	}
	return out
}

func TestSkillLinks(t *testing.T) {
	root := skillRoot(t)
	for _, file := range markdownFiles(t) {
		for _, m := range linkRe.FindAllStringSubmatch(string(readFile(t, file)), -1) {
			target := m[1]
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			pathPart, anchor, _ := strings.Cut(target, "#")
			resolved := file
			if pathPart != "" {
				resolved = filepath.Clean(filepath.Join(filepath.Dir(file), pathPart))
			}
			rel, err := filepath.Rel(root, resolved)
			if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
				t.Errorf("%s: link %q leaves the Skill package", file, target)
				continue
			}
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("%s: link %q does not resolve", file, target)
				continue
			}
			if anchor != "" && strings.HasSuffix(resolved, ".md") && !anchors(t, resolved)[anchor] {
				t.Errorf("%s: anchor %q not found in %s", file, anchor, rel)
			}
		}
	}
}

// Container home directories that are part of images, not machine paths.
var allowedHomes = map[string]bool{"/home/nonroot": true}

var homeRe = regexp.MustCompile(`/home/[a-z][a-z0-9_.-]*`)

var forbiddenPathPatterns = []*regexp.Regexp{
	regexp.MustCompile(`~/\.claude`),
	regexp.MustCompile(`[A-Za-z]:\\\\`),
	regexp.MustCompile(`\$\{CLAUDE_SKILL_DIR\}`),
	regexp.MustCompile(`management/v\d`),
	regexp.MustCompile(`\bG0\d\d\b`),
	regexp.MustCompile(`(?i)architecture(\.md)?\s*§`),
}

func TestSkillPaths(t *testing.T) {
	root := skillRoot(t)
	self, _ := filepath.Abs("skill_test.go")
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == self {
			return err
		}
		base := filepath.Base(p)
		if base == "go.sum" || base == "package-lock.json" || base == "uv.lock" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		for _, re := range forbiddenPathPatterns {
			if loc := re.Find(b); loc != nil {
				t.Errorf("%s contains forbidden reference %q", rel, loc)
			}
		}
		for _, home := range homeRe.FindAll(b, -1) {
			if !allowedHomes[string(home)] {
				t.Errorf("%s contains machine-specific path %q", rel, home)
			}
		}
		return nil
	})
}

var (
	catalogRowRe = regexp.MustCompile(`^\|\s*([A-Z]+-[0-9]+)\s*\|(.*)\|\s*$`)
	checkIDRe    = regexp.MustCompile(`\b[SGTIX]-[A-Z][A-Z0-9-]*[A-Z0-9]\b`)
	goTestRe     = regexp.MustCompile(`\bTest[A-Za-z0-9_]+\b`)
	pyTestRe     = regexp.MustCompile(`\btest_i_[a-z0-9_]+\b`)
	stageRe      = regexp.MustCompile(`\bSTAGE-[A-Z-]+[A-Z]\b`)
)

func TestSkillTrace(t *testing.T) {
	root := skillRoot(t)
	catalog := string(readFile(t, filepath.Join(root, "references/requirements-catalog.md")))
	harness := string(readFile(t, filepath.Join(root, "validation/run.sh")))
	followUp := string(readFile(t, filepath.Join(root, FollowUpSource)))

	var goSources, pySources strings.Builder
	filepath.WalkDir(filepath.Join(root, "validation/isolation"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") {
			goSources.Write(readFile(t, p))
		}
		return nil
	})
	filepath.WalkDir(filepath.Join(root, "templates/intelligence/intelligence/tests"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".py") {
			pySources.Write(readFile(t, p))
		}
		return nil
	})

	seen := map[string]bool{}
	rows := 0
	for _, line := range strings.Split(catalog, "\n") {
		m := catalogRowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rows++
		id := m[1]
		if seen[id] {
			t.Errorf("duplicate requirement ID %s", id)
		}
		seen[id] = true
		cells := strings.Split(m[2], "|")
		if len(cells) != 4 {
			t.Errorf("%s: expected columns Requirement | Class | Resource | Verification", id)
			continue
		}
		class, resource, verification := strings.TrimSpace(cells[1]), cells[2], cells[3]
		links := linkRe.FindAllStringSubmatch(resource, -1)
		if len(links) == 0 {
			t.Errorf("%s: no resource link", id)
		}
		for _, l := range links {
			target, _, _ := strings.Cut(l[1], "#")
			if _, err := os.Stat(filepath.Join(root, "references", target)); err != nil {
				t.Errorf("%s: resource %q does not resolve", id, l[1])
			}
		}
		switch class {
		case "T":
			ids := checkIDRe.FindAllString(verification, -1)
			if len(ids) == 0 {
				t.Errorf("%s: class T needs a harness check ID", id)
			}
			for _, c := range ids {
				if !strings.Contains(harness, c) {
					t.Errorf("%s: check %s is not defined in validation/run.sh", id, c)
				}
			}
		case "F":
			names := goTestRe.FindAllString(verification, -1)
			pys := pyTestRe.FindAllString(verification, -1)
			if len(names)+len(pys) == 0 {
				t.Errorf("%s: class F needs a fixture test name", id)
			}
			for _, n := range names {
				if !strings.Contains(goSources.String(), "func "+n+"(") {
					t.Errorf("%s: fixture test %s does not exist", id, n)
				}
			}
			for _, n := range pys {
				if !strings.Contains(pySources.String(), "def "+n) {
					t.Errorf("%s: intelligence test %s does not exist", id, n)
				}
			}
		case "P":
			stages := stageRe.FindAllString(verification, -1)
			if len(stages) == 0 || len(strings.TrimSpace(stageRe.ReplaceAllString(verification, ""))) < 10 {
				t.Errorf("%s: class P needs a foundation stage and a verification method", id)
			}
			for _, s := range stages {
				if !strings.Contains(followUp, "## "+s) {
					t.Errorf("%s: stage %s has no section in %s", id, s, FollowUpSource)
				}
			}
		default:
			t.Errorf("%s: class must be T, F or P (got %q)", id, class)
		}
	}
	if rows < 40 {
		t.Fatalf("requirements catalog has only %d rows", rows)
	}
}
