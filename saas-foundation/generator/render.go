package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

// SkillVersion is the archetype Skill version. The static suite asserts it
// equals SKILL.md frontmatter metadata.version.
const SkillVersion = "0.1.0"

// Write modes for targets.
const (
	ModeFile        = "file"
	ModeEnsureLines = "ensure-lines"
)

// Target is one intended output file.
type Target struct {
	Path    string // slash-separated, relative to the destination
	Content []byte // intended content (for ensure-lines: the managed block)
	Mode    string
	Exec    bool
	Lines   []string // ensure-lines: the required lines
}

// TemplateData is passed to *.tmpl files.
type TemplateData struct {
	Config
	SkillVersion string
}

var templateFuncs = template.FuncMap{
	"upper": strings.ToUpper,
	"join":  strings.Join,
}

// FollowUpSource is copied verbatim to FollowUpTarget.
const (
	FollowUpSource = "checklists/foundation-follow-up.md"
	FollowUpTarget = "docs/foundation/FOLLOW-UP.md"
)

const ensureLinesBegin = "# >>> project-archetype managed block (required lines; edit outside this block)"
const ensureLinesEnd = "# <<< project-archetype managed block"

// reservedTargets are never emitted. A template that would emit one is a
// defect and is refused.
var reservedTargets = []string{"AGENTS.md", "CLAUDE.md", "README.md", ".env", "docs/ARCHITECTURE.md"}
var reservedPrefixes = []string{"management/", "skills/", ".git/"}

// IsReserved reports whether a target path is reserved.
func IsReserved(p string) bool {
	for _, r := range reservedTargets {
		if p == r {
			return true
		}
	}
	for _, pre := range reservedPrefixes {
		if strings.HasPrefix(p, pre) || p+"/" == pre {
			return true
		}
	}
	return false
}

// mapTemplatePath converts a template-relative path into a target path:
// segments starting with "dot_" become dot-files and a ".tmpl" suffix is
// dropped. It reports whether the file is a template.
func mapTemplatePath(rel string) (string, bool) {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, "dot_") {
			parts[i] = "." + strings.TrimPrefix(p, "dot_")
		}
	}
	out := strings.Join(parts, "/")
	if strings.HasSuffix(out, ".tmpl") {
		return strings.TrimSuffix(out, ".tmpl"), true
	}
	return out, false
}

// BuildTargets renders the intended output for a configuration. It never
// touches the destination.
func BuildTargets(skillDir string, cfg Config) ([]Target, error) {
	data := TemplateData{Config: cfg, SkillVersion: SkillVersion}
	sets := []string{"templates/base"}
	if cfg.IntelligenceEnabled {
		sets = append(sets, "templates/intelligence")
	}
	byPath := map[string]Target{}
	add := func(t Target, origin string) error {
		if err := checkRelative(t.Path); err != nil {
			return fmt.Errorf("template %s: %w", origin, err)
		}
		if IsReserved(t.Path) {
			return fmt.Errorf("template %s would emit reserved path %s", origin, t.Path)
		}
		if _, dup := byPath[t.Path]; dup {
			return fmt.Errorf("template %s duplicates target %s", origin, t.Path)
		}
		byPath[t.Path] = t
		return nil
	}

	for _, set := range sets {
		root := filepath.Join(skillDir, filepath.FromSlash(set))
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return fmt.Errorf("template %s is a symlink; templates must be regular files", p)
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if rel == "gitignore.lines" {
				lines := nonEmptyLines(string(raw))
				return add(Target{Path: ".gitignore", Mode: ModeEnsureLines, Lines: lines, Content: []byte(ensureBlock(lines))}, set+"/"+rel)
			}
			target, isTmpl := mapTemplatePath(rel)
			content := raw
			if isTmpl {
				tpl, err := template.New(rel).Funcs(templateFuncs).Option("missingkey=error").Parse(string(raw))
				if err != nil {
					return fmt.Errorf("parse template %s: %w", rel, err)
				}
				var buf bytes.Buffer
				if err := tpl.Execute(&buf, data); err != nil {
					return fmt.Errorf("render template %s: %w", rel, err)
				}
				content = buf.Bytes()
			}
			return add(Target{Path: target, Content: content, Mode: ModeFile, Exec: info.Mode()&0o111 != 0}, set+"/"+rel)
		})
		if err != nil {
			return nil, err
		}
	}

	followUp, err := os.ReadFile(filepath.Join(skillDir, filepath.FromSlash(FollowUpSource)))
	if err != nil {
		return nil, fmt.Errorf("read follow-up checklist: %w", err)
	}
	if err := add(Target{Path: FollowUpTarget, Content: followUp, Mode: ModeFile}, FollowUpSource); err != nil {
		return nil, err
	}

	out := make([]Target, 0, len(byPath))
	for _, t := range byPath {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func ensureBlock(lines []string) string {
	return ensureLinesBegin + "\n" + strings.Join(lines, "\n") + "\n" + ensureLinesEnd + "\n"
}

// checkRelative rejects absolute paths and any traversal.
func checkRelative(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return fmt.Errorf("path %q is not a clean relative path", p)
	}
	if path.Clean(p) != p {
		return fmt.Errorf("path %q is not clean", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return fmt.Errorf("path %q escapes or is malformed", p)
		}
	}
	return nil
}

// OutputID is the stable output identity: it depends only on canonical
// non-secret inputs, the Skill version and the intended content, never on
// the destination state.
func OutputID(cfg Config, targets []Target) (string, error) {
	canon, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	fmt.Fprintf(h, "inputs\x00%s\x00version\x00%s\x00", canon, SkillVersion)
	for _, t := range targets {
		sum := sha256.Sum256(t.Content)
		fmt.Fprintf(h, "target\x00%s\x00%s\x00%s\x00%t\x00", t.Path, hex.EncodeToString(sum[:]), t.Mode, t.Exec)
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// ManifestFile describes one intended target in the manifest.
type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"`
	Exec   bool   `json:"executable,omitempty"`
}

// Manifest is the content-addressed output manifest. It deliberately has no
// action states, timestamps, secrets or absolute paths.
type Manifest struct {
	Schema       string         `json:"schema"`
	OutputID     string         `json:"output_id"`
	SkillVersion string         `json:"skill_version"`
	Inputs       Config         `json:"inputs"`
	Files        []ManifestFile `json:"files"`
	FollowUp     string         `json:"follow_up"`
}

// BuildManifest returns the manifest target for an output.
func BuildManifest(cfg Config, id string, targets []Target) (Target, error) {
	m := Manifest{Schema: "project-archetype/manifest/v1", OutputID: id, SkillVersion: SkillVersion, Inputs: cfg, FollowUp: FollowUpTarget}
	for _, t := range targets {
		sum := sha256.Sum256(t.Content)
		m.Files = append(m.Files, ManifestFile{Path: t.Path, SHA256: hex.EncodeToString(sum[:]), Mode: t.Mode, Exec: t.Exec})
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Target{}, err
	}
	b = append(b, '\n')
	return Target{Path: OutputDir(id) + "/manifest.json", Content: b, Mode: ModeFile}, nil
}

// OutputDir is the destination-relative directory for one output identity.
func OutputDir(id string) string { return ".archetype/outputs/" + id }
