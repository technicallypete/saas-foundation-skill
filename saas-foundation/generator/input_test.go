package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseValidInputs(t *testing.T) {
	for _, path := range []string{"../examples/neutral.input.json", "../validation/inputs/alpha.json", "../validation/inputs/bravo.json"} {
		cfg, err := ParseInput(readFile(t, path))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if cfg.ProjectSlug == "" || cfg.CustomDomainCNAMETarget == "" || cfg.IdentityProvider == "" {
			t.Fatalf("%s: defaults not applied: %+v", path, cfg)
		}
	}
	bravo, _ := ParseInput(readFile(t, "../validation/inputs/bravo.json"))
	if bravo.Ports.Web != 3200 || bravo.Ports.Intelligence != 0 || bravo.ProjectSlug != "harbor-desk" {
		t.Fatalf("bravo resolved incorrectly: %+v", bravo)
	}
	alpha, _ := ParseInput(readFile(t, "../validation/inputs/alpha.json"))
	if alpha.Ports.Intelligence != 8100 || alpha.CustomDomainCNAMETarget != "custom.atlas.example.test" {
		t.Fatalf("alpha resolved incorrectly: %+v", alpha)
	}
}

// Every invalid fixture used by the harness (G-INVALID) must be rejected with
// a specific, explanatory message.
func TestParseInvalidInputs(t *testing.T) {
	want := map[string]string{
		"malformed-json.json":                    "not a valid JSON object",
		"missing-product-name.json":              `missing required field "product_name"`,
		"missing-base-domain.json":               `missing required field "platform_base_domain"`,
		"missing-database-name.json":             `missing required field "database_name"`,
		"missing-intelligence-flag.json":         `missing required field "intelligence_enabled"`,
		"non-boolean-intelligence.json":          "must be a JSON boolean",
		"null-intelligence-flag.json":            "null is not allowed",
		"unknown-field.json":                     `unknown field "color"`,
		"bad-domain.json":                        "platform_base_domain",
		"cname-equals-base.json":                 "must not equal platform_base_domain",
		"reserved-database-name.json":            "is reserved",
		"overlong-database-name.json":            "database_name",
		"port-collision.json":                    "port collision",
		"port-out-of-range.json":                 "outside 1024-65535",
		"malformed-model-identity.json":          "provider:model",
		"gateway-as-provider.json":               "gateway",
		"duplicate-fallback.json":                "repeats",
		"models-with-intelligence-disabled.json": "intelligence is disabled",
		"injection-in-product-name.json":         "product_name",
		"secret-looking-field.json":              "looks like a secret",
		"fallbacks-without-primary.json":         "without ai_models.chat",
	}
	files, err := filepath.Glob("../validation/inputs/invalid/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(want) {
		t.Fatalf("expected %d invalid fixtures, found %d", len(want), len(files))
	}
	for _, f := range files {
		name := filepath.Base(f)
		expected, ok := want[name]
		if !ok {
			t.Errorf("invalid fixture %s has no expectation", name)
			continue
		}
		_, err := ParseInput(readFile(t, f))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("%s: error %q does not mention %q", name, err, expected)
		}
	}
}

func TestAllErrorsReportedTogether(t *testing.T) {
	_, err := ParseInput([]byte(`{"product_name":"","platform_base_domain":"X","database_name":"template1","color":1}`))
	if err == nil {
		t.Fatal("expected errors")
	}
	verrs := err.(ValidationErrors)
	if len(verrs) < 4 {
		t.Fatalf("expected every problem reported together, got %v", verrs)
	}
}

func TestDeriveSlug(t *testing.T) {
	cases := map[string]string{
		"Sample Atlas":       "sample-atlas",
		"Harbor Desk":        "harbor-desk",
		"  Mixed__Case 42 !": "mixed-case-42",
		"O'Neil & Sons":      "o-neil-sons",
	}
	for in, want := range cases {
		if got := DeriveSlug(in); got != want {
			t.Errorf("DeriveSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckModelIdentity(t *testing.T) {
	ok := []string{"anthropic:model-x", "openai:gpt-sample-1", "vendor:family/model:free"}
	bad := []string{"", "nocolon", ":model", "provider:", "Upper:model", "openrouter:anything", "provider:has space"}
	for _, id := range ok {
		if msg := CheckModelIdentity(id); msg != "" {
			t.Errorf("%q rejected: %s", id, msg)
		}
	}
	for _, id := range bad {
		if CheckModelIdentity(id) == "" {
			t.Errorf("%q accepted", id)
		}
	}
}
