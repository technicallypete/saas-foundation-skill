package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const skillDir = ".."

func mustConfig(t *testing.T, path string) Config {
	t.Helper()
	cfg, err := ParseInput(readFile(t, path))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func targetMap(ts []Target) map[string]Target {
	m := map[string]Target{}
	for _, t := range ts {
		m[t.Path] = t
	}
	return m
}

func TestTargetsRespectIntelligenceToggle(t *testing.T) {
	alpha := mustConfig(t, "../validation/inputs/alpha.json")
	bravo := mustConfig(t, "../validation/inputs/bravo.json")
	on, err := BuildTargets(skillDir, alpha)
	if err != nil {
		t.Fatal(err)
	}
	off, err := BuildTargets(skillDir, bravo)
	if err != nil {
		t.Fatal(err)
	}
	onMap, offMap := targetMap(on), targetMap(off)
	if _, ok := onMap["intelligence/pyproject.toml"]; !ok {
		t.Error("intelligence files missing when enabled")
	}
	for p := range offMap {
		if strings.HasPrefix(p, "intelligence/") || strings.Contains(p, "intelligence.Dockerfile") || strings.HasPrefix(p, "contracts/intelligence/") {
			t.Errorf("intelligence file %s rendered while disabled", p)
		}
	}
	for _, needle := range []string{"AI_MODEL_", "OPENROUTER_API_KEY", "INTELLIGENCE_"} {
		if bytes.Contains(offMap[".env.example"].Content, []byte(needle)) || bytes.Contains(offMap["infra/compose/local.yml"].Content, []byte(needle)) {
			t.Errorf("%s present while intelligence disabled", needle)
		}
	}
}

func TestNoReservedTargetsAndManagedGitignore(t *testing.T) {
	ts, err := BuildTargets(skillDir, mustConfig(t, "../validation/inputs/alpha.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range ts {
		if IsReserved(tg.Path) {
			t.Errorf("reserved target %s", tg.Path)
		}
	}
	m := targetMap(ts)
	gi, ok := m[".gitignore"]
	if !ok || gi.Mode != ModeEnsureLines || len(gi.Lines) == 0 {
		t.Fatalf("gitignore not ensure-lines: %+v", gi)
	}
	if _, ok := m[FollowUpTarget]; !ok {
		t.Fatal("follow-up checklist not rendered")
	}
}

// Identity literals may appear only in .env.example (plus generator-owned
// .archetype manifests): source, Compose and docs resolve them at runtime.
func TestIdentityLiteralsConfinedToEnvExample(t *testing.T) {
	cfg := mustConfig(t, "../validation/inputs/alpha.json")
	ts, err := BuildTargets(skillDir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	literals := []string{cfg.ProductName, cfg.PlatformBaseDomain, cfg.CustomDomainCNAMETarget, cfg.DatabaseName, cfg.ProjectSlug, strings.ToLower(cfg.ProductName)}
	for _, tg := range ts {
		if tg.Path == ".env.example" {
			continue
		}
		for _, lit := range literals {
			if bytes.Contains(bytes.ToLower(tg.Content), []byte(strings.ToLower(lit))) {
				t.Errorf("%s contains identity literal %q", tg.Path, lit)
			}
		}
	}
}

func TestOutputIDStateIndependentAndSensitive(t *testing.T) {
	cfg := mustConfig(t, "../validation/inputs/alpha.json")
	ts, _ := BuildTargets(skillDir, cfg)
	id1, _ := OutputID(cfg, ts)
	id2, _ := OutputID(cfg, ts)
	if id1 != id2 || len(id1) != 16 {
		t.Fatalf("output ID unstable: %s %s", id1, id2)
	}
	mutations := []func(*Config){
		func(c *Config) { c.ProductName = "Other Name" },
		func(c *Config) { c.PlatformBaseDomain = "other.example.test" },
		func(c *Config) { c.DatabaseName = "other_app" },
		func(c *Config) { c.Ports.Web = 3999 },
		func(c *Config) { c.IdentityProvider = "other" },
	}
	for i, mutate := range mutations {
		c := cfg
		mutate(&c)
		mts, err := BuildTargets(skillDir, c)
		if err != nil {
			t.Fatal(err)
		}
		mid, _ := OutputID(c, mts)
		if mid == id1 {
			t.Errorf("mutation %d did not change output ID", i)
		}
	}
}

func renderInto(t *testing.T, input, dest string, extra ...string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	args := append([]string{"render", "--skill", skillDir, "--input", input, "--dest", dest}, extra...)
	code := run(args, &out, &errOut)
	return code, out.String() + errOut.String()
}

type snapshot map[string]string

func snap(t *testing.T, dir string) snapshot {
	t.Helper()
	s := snapshot{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if info.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		s[rel] = string(b) + "|" + info.ModTime().Format(time.RFC3339Nano)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func equalSnap(a, b snapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestRepeatCleanWritesNothing(t *testing.T) {
	dest := t.TempDir()
	code, out := renderInto(t, "../validation/inputs/bravo.json", dest)
	if code != exitOK || !strings.Contains(out, "create\t") {
		t.Fatalf("first render: %d\n%s", code, out)
	}
	before := snap(t, dest)
	time.Sleep(20 * time.Millisecond)
	code, out = renderInto(t, "../validation/inputs/bravo.json", dest)
	if code != exitOK {
		t.Fatalf("second render: %d\n%s", code, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, "output_id\t") || strings.HasPrefix(line, "result\t") {
			continue
		}
		if !strings.HasPrefix(line, "unchanged\t") {
			t.Errorf("repeat produced action: %s", line)
		}
	}
	if !equalSnap(before, snap(t, dest)) {
		t.Fatal("repeat render modified the destination")
	}
}

func TestConflictsNeverOverwriteAndRepeatIsStable(t *testing.T) {
	dest := t.TempDir()
	sentinels := map[string]string{
		"README.md":               "sentinel readme\n",
		"AGENTS.md":               "sentinel agents\n",
		".env":                    "SECRET=sentinel\n",
		".gitignore":              "custom-line\nno-trailing-newline",
		"infra/compose/local.yml": "sentinel: compose\n",
		"docs/ARCHITECTURE.md":    "sentinel architecture\n",
		"management/x.md":         "sentinel management\n",
		"skills/other/SKILL.md":   "sentinel skill\n",
	}
	for p, c := range sentinels {
		full := filepath.Join(dest, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	code, out := renderInto(t, "../validation/inputs/alpha.json", dest)
	if code != exitConflicts {
		t.Fatalf("expected conflicts exit, got %d\n%s", code, out)
	}
	for p, c := range sentinels {
		got, _ := os.ReadFile(filepath.Join(dest, p))
		if p == ".gitignore" {
			if !strings.HasPrefix(string(got), c) || !strings.Contains(string(got), ".env\n") {
				t.Errorf(".gitignore not merged safely: %q", got)
			}
			continue
		}
		if string(got) != c {
			t.Errorf("sentinel %s modified", p)
		}
	}
	if !strings.Contains(out, "conflict\tinfra/compose/local.yml") {
		t.Fatalf("conflict not reported:\n%s", out)
	}
	var id string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "output_id\t") {
			id = strings.TrimPrefix(l, "output_id\t")
		}
	}
	if _, err := os.Stat(filepath.Join(dest, OutputDir(id), "proposed/infra/compose/local.yml")); err != nil {
		t.Fatalf("proposal missing: %v", err)
	}
	before := snap(t, dest)
	time.Sleep(20 * time.Millisecond)
	code2, out2 := renderInto(t, "../validation/inputs/alpha.json", dest)
	if code2 != exitConflicts || !strings.Contains(out2, "output_id\t"+id) {
		t.Fatalf("repeat conflict run: %d\n%s", code2, out2)
	}
	if !equalSnap(before, snap(t, dest)) {
		t.Fatal("repeat conflict run wrote to the destination")
	}
}

func TestOutputIDIndependentOfDestinationState(t *testing.T) {
	ids := map[string]bool{}
	for _, prep := range []func(string){
		func(string) {},
		func(d string) { os.WriteFile(filepath.Join(d, "README.md"), []byte("x"), 0o644) },
		func(d string) { renderInto(t, "../validation/inputs/alpha.json", d) },
	} {
		dest := t.TempDir()
		prep(dest)
		var out bytes.Buffer
		run([]string{"plan", "--skill", skillDir, "--input", "../validation/inputs/alpha.json", "--dest", dest}, &out, &bytes.Buffer{})
		for _, l := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(l, "output_id\t") {
				ids[l] = true
			}
		}
	}
	if len(ids) != 1 {
		t.Fatalf("output_id depends on destination state: %v", ids)
	}
}

func TestPlanWritesNothing(t *testing.T) {
	dest := t.TempDir()
	var out bytes.Buffer
	if code := run([]string{"plan", "--skill", skillDir, "--input", "../validation/inputs/alpha.json", "--dest", dest}, &out, &bytes.Buffer{}); code != exitOK {
		t.Fatalf("plan exit %d", code)
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 0 {
		t.Fatalf("plan wrote %d entries", len(entries))
	}
}

func TestInvalidInputWritesNothing(t *testing.T) {
	dest := t.TempDir()
	code, _ := renderInto(t, "../validation/inputs/invalid/gateway-as-provider.json", dest)
	if code != exitInvalid {
		t.Fatalf("expected exit 2, got %d", code)
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 0 {
		t.Fatal("invalid input wrote to the destination")
	}
}

// intelligence_enabled accepts only an explicit JSON boolean: null is
// rejected by validate, plan and render with exit 2 and no writes, while
// true and false remain valid.
func TestIntelligenceFlagNullRejectedTrueFalseAccepted(t *testing.T) {
	dir := t.TempDir()
	write := func(name, flag string) string {
		p := filepath.Join(dir, name)
		body := `{"product_name":"Flag Probe","platform_base_domain":"flag.example.test","database_name":"flag_app","intelligence_enabled":` + flag + `}`
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	null, yes, no := write("null.json", "null"), write("true.json", "true"), write("false.json", "false")

	for _, cmd := range []string{"validate", "plan", "render"} {
		dest := t.TempDir()
		if err := os.WriteFile(filepath.Join(dest, "sentinel"), []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
		before := snap(t, dest)
		var out bytes.Buffer
		args := []string{cmd, "--skill", skillDir, "--input", null}
		if cmd != "validate" {
			args = append(args, "--dest", dest)
		}
		if code := run(args, &out, &out); code != exitInvalid {
			t.Errorf("%s with null: exit %d, want %d\n%s", cmd, code, exitInvalid, out.String())
		}
		if !strings.Contains(out.String(), "null is not allowed") {
			t.Errorf("%s with null: missing explanatory error\n%s", cmd, out.String())
		}
		if !equalSnap(before, snap(t, dest)) {
			t.Errorf("%s with null wrote to the destination", cmd)
		}
	}

	for _, c := range []struct {
		input string
		want  bool
	}{{yes, true}, {no, false}} {
		cfg, err := ParseInput(readFile(t, c.input))
		if err != nil || cfg.IntelligenceEnabled != c.want {
			t.Fatalf("intelligence_enabled=%t rejected or misread: %+v %v", c.want, cfg.IntelligenceEnabled, err)
		}
		for _, cmd := range []string{"validate", "plan", "render"} {
			var out bytes.Buffer
			args := []string{cmd, "--skill", skillDir, "--input", c.input}
			if cmd != "validate" {
				args = append(args, "--dest", t.TempDir())
			}
			if code := run(args, &out, &out); code != exitOK {
				t.Errorf("%s with intelligence_enabled=%t: exit %d\n%s", cmd, c.want, code, out.String())
			}
		}
	}
}

func TestRefusesSymlinks(t *testing.T) {
	dest := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dest, "infra")); err != nil {
		t.Fatal(err)
	}
	code, out := renderInto(t, "../validation/inputs/bravo.json", dest)
	if code != exitInvalid || !strings.Contains(out, "symlink") {
		t.Fatalf("symlinked parent not refused: %d\n%s", code, out)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("wrote through a symlink")
	}
	dest2 := t.TempDir()
	os.MkdirAll(filepath.Join(dest2, "docs/foundation"), 0o755)
	os.Symlink(filepath.Join(outside, "x"), filepath.Join(dest2, "docs/foundation/VENDORS.md"))
	if code, _ := renderInto(t, "../validation/inputs/bravo.json", dest2); code != exitInvalid {
		t.Fatalf("symlinked target not refused: %d", code)
	}
}

func TestRefusesDestinationInsideSkill(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"render", "--skill", skillDir, "--input", "../validation/inputs/bravo.json", "--dest", "../references"}, &out, &out)
	if code != exitInvalid || !strings.Contains(out.String(), "Skill package") {
		t.Fatalf("destination inside the Skill not refused: %d %s", code, out.String())
	}
}

func TestReportOutsideDestinationOnly(t *testing.T) {
	dest := t.TempDir()
	var out bytes.Buffer
	code := run([]string{"render", "--skill", skillDir, "--input", "../validation/inputs/bravo.json", "--dest", dest, "--report", filepath.Join(dest, "r.json")}, &out, &out)
	if code != exitInvalid {
		t.Fatalf("report inside destination not refused: %d", code)
	}
	reportDir := t.TempDir()
	report := filepath.Join(reportDir, "r.json")
	code = run([]string{"render", "--skill", skillDir, "--input", "../validation/inputs/bravo.json", "--dest", dest, "--report", report}, &out, &out)
	if code != exitOK {
		t.Fatalf("render with report: %d", code)
	}
	var rep Report
	if err := json.Unmarshal(readFile(t, report), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.OutputID == "" || len(rep.Actions) == 0 || rep.Actions[0].Action != ActionCreate {
		t.Fatalf("unexpected report: %+v", rep)
	}
}

func TestManifestHasNoActionsTimestampsOrAbsolutePaths(t *testing.T) {
	cfg := mustConfig(t, "../validation/inputs/alpha.json")
	ts, _ := BuildTargets(skillDir, cfg)
	id, _ := OutputID(cfg, ts)
	m, err := BuildManifest(cfg, id, ts)
	if err != nil {
		t.Fatal(err)
	}
	s := string(m.Content)
	for _, bad := range []string{`"action"`, "unchanged", `"create"`, "/home/", "/tmp/", "T00:", "password"} {
		if strings.Contains(s, bad) {
			t.Errorf("manifest contains %q", bad)
		}
	}
}
