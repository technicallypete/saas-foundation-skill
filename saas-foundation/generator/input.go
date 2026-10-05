package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Capabilities are the initial AI capability classes, in canonical order.
var Capabilities = []string{"chat", "fast", "reasoning", "extraction", "embedding"}

// gatewayProviders are model gateways. They must never appear as the
// provider half of a canonical provider:model identity.
var gatewayProviders = map[string]bool{"openrouter": true}

var (
	slugRe        = regexp.MustCompile(`^[a-z][a-z0-9-]{1,38}[a-z0-9]$`)
	dbNameRe      = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,47}$`)
	labelRe       = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	identityIdpRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	providerRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	modelRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:+-]{0,127}$`)
	secretKeyRe   = regexp.MustCompile(`(?i)(password|secret|token|api_?key|credential)`)
)

var reservedDatabaseNames = map[string]bool{"postgres": true, "template0": true, "template1": true}

var objectStoreChoices = map[string]bool{"r2": true, "s3": true, "s3-compatible": true}

// Ports holds host port assignments.
type Ports struct {
	Web          int `json:"web"`
	Core         int `json:"core"`
	Intelligence int `json:"intelligence,omitempty"`
	Postgres     int `json:"postgres"`
	Redis        int `json:"redis"`
	MinioAPI     int `json:"minio_api"`
	MinioConsole int `json:"minio_console"`
}

// ModelSetting is one AI capability configuration.
type ModelSetting struct {
	Capability string   `json:"capability"`
	Primary    string   `json:"primary"`
	Fallbacks  []string `json:"fallbacks,omitempty"`
}

// Config is the resolved, validated input with defaults applied. It is the
// canonical, non-secret description that feeds output identity.
type Config struct {
	ProductName             string         `json:"product_name"`
	ProjectSlug             string         `json:"project_slug"`
	PlatformBaseDomain      string         `json:"platform_base_domain"`
	CustomDomainCNAMETarget string         `json:"custom_domain_cname_target"`
	DatabaseName            string         `json:"database_name"`
	IntelligenceEnabled     bool           `json:"intelligence_enabled"`
	IdentityProvider        string         `json:"identity_provider"`
	ProductionObjectStore   string         `json:"production_object_store"`
	Ports                   Ports          `json:"ports"`
	AIModels                []ModelSetting `json:"ai_models,omitempty"`
}

// ValidationErrors collects every input problem so all are reported at once.
type ValidationErrors []string

func (v ValidationErrors) Error() string { return strings.Join(v, "; ") }

func (v *ValidationErrors) add(format string, args ...any) {
	*v = append(*v, fmt.Sprintf(format, args...))
}

var knownTopLevel = map[string]bool{
	"product_name": true, "project_slug": true, "platform_base_domain": true,
	"custom_domain_cname_target": true, "database_name": true, "intelligence_enabled": true,
	"identity_provider": true, "production_object_store": true, "ports": true, "ai_models": true,
}

var portKeys = []string{"web", "core", "intelligence", "postgres", "redis", "minio_api", "minio_console"}

var defaultPorts = map[string]int{
	"web": 3100, "core": 8180, "intelligence": 8100, "postgres": 5532,
	"redis": 6479, "minio_api": 9100, "minio_console": 9101,
}

// ParseInput validates raw JSON input and returns the resolved Config. All
// problems are collected and returned together; no partial Config is usable.
func ParseInput(raw []byte) (Config, error) {
	var errs ValidationErrors
	var cfg Config

	var fields map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&fields); err != nil {
		errs.add("input is not a valid JSON object: %v", err)
		return cfg, errs
	}
	if dec.More() {
		errs.add("input contains trailing data after the JSON object")
		return cfg, errs
	}

	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !knownTopLevel[k] {
			if secretKeyRe.MatchString(k) {
				errs.add("field %q looks like a secret; secrets are never inputs", k)
			} else {
				errs.add("unknown field %q", k)
			}
		}
	}

	str := func(name string, required bool) (string, bool) {
		v, ok := fields[name]
		if !ok {
			if required {
				errs.add("missing required field %q", name)
			}
			return "", false
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			errs.add("field %q must be a string", name)
			return "", false
		}
		return s, true
	}

	// product_name
	if name, ok := str("product_name", true); ok {
		if msg := checkProductName(name); msg != "" {
			errs.add("product_name %s", msg)
		} else {
			cfg.ProductName = name
		}
	}

	// project_slug (derived when absent)
	if slug, ok := str("project_slug", false); ok {
		if !slugRe.MatchString(slug) {
			errs.add("project_slug %q must match %s", slug, slugRe)
		} else {
			cfg.ProjectSlug = slug
		}
	} else if cfg.ProductName != "" {
		slug := DeriveSlug(cfg.ProductName)
		if !slugRe.MatchString(slug) {
			errs.add("cannot derive a valid project_slug from product_name (got %q); supply project_slug explicitly", slug)
		} else {
			cfg.ProjectSlug = slug
		}
	}

	// platform_base_domain
	if d, ok := str("platform_base_domain", true); ok {
		if msg := checkHostname(d); msg != "" {
			errs.add("platform_base_domain %q %s", d, msg)
		} else {
			cfg.PlatformBaseDomain = d
		}
	}

	// custom_domain_cname_target (defaulted)
	if t, ok := str("custom_domain_cname_target", false); ok {
		if msg := checkHostname(t); msg != "" {
			errs.add("custom_domain_cname_target %q %s", t, msg)
		} else if t == cfg.PlatformBaseDomain {
			errs.add("custom_domain_cname_target must not equal platform_base_domain")
		} else {
			cfg.CustomDomainCNAMETarget = t
		}
	} else if cfg.PlatformBaseDomain != "" {
		cfg.CustomDomainCNAMETarget = "custom." + cfg.PlatformBaseDomain
	}

	// database_name
	if db, ok := str("database_name", true); ok {
		switch {
		case !dbNameRe.MatchString(db):
			errs.add("database_name %q must match %s (at most 48 characters, leaving room for role suffixes)", db, dbNameRe)
		case reservedDatabaseNames[db]:
			errs.add("database_name %q is reserved", db)
		default:
			cfg.DatabaseName = db
		}
	}

	// intelligence_enabled (explicit boolean)
	// Decoded through a pointer: JSON null would otherwise decode into a bool
	// without error and silently become false.
	intelligenceKnown := false
	if v, ok := fields["intelligence_enabled"]; !ok {
		errs.add("missing required field %q", "intelligence_enabled")
	} else {
		var flag *bool
		if err := json.Unmarshal(v, &flag); err != nil || flag == nil {
			errs.add("field %q must be a JSON boolean (true or false); null is not allowed", "intelligence_enabled")
		} else {
			cfg.IntelligenceEnabled = *flag
			intelligenceKnown = true
		}
	}

	// identity_provider
	cfg.IdentityProvider = "oidc"
	if idp, ok := str("identity_provider", false); ok {
		if !identityIdpRe.MatchString(idp) {
			errs.add("identity_provider %q must match %s", idp, identityIdpRe)
		} else {
			cfg.IdentityProvider = idp
		}
	}

	// production_object_store
	cfg.ProductionObjectStore = "s3-compatible"
	if store, ok := str("production_object_store", false); ok {
		if !objectStoreChoices[store] {
			errs.add("production_object_store %q must be one of r2, s3, s3-compatible", store)
		} else {
			cfg.ProductionObjectStore = store
		}
	}

	// ports
	ports := map[string]int{}
	for k, v := range defaultPorts {
		ports[k] = v
	}
	if rawPorts, ok := fields["ports"]; ok {
		var given map[string]json.RawMessage
		if err := json.Unmarshal(rawPorts, &given); err != nil {
			errs.add("field %q must be an object", "ports")
		} else {
			gk := make([]string, 0, len(given))
			for k := range given {
				gk = append(gk, k)
			}
			sort.Strings(gk)
			for _, k := range gk {
				if _, known := defaultPorts[k]; !known {
					errs.add("unknown port %q (known: %s)", k, strings.Join(portKeys, ", "))
					continue
				}
				var p int
				if err := json.Unmarshal(given[k], &p); err != nil {
					errs.add("ports.%s must be an integer", k)
					continue
				}
				if p < 1024 || p > 65535 {
					errs.add("ports.%s = %d is outside 1024-65535", k, p)
					continue
				}
				if k == "intelligence" && intelligenceKnown && !cfg.IntelligenceEnabled {
					errs.add("ports.intelligence is set but intelligence is disabled")
					continue
				}
				ports[k] = p
			}
		}
	}
	seen := map[int]string{}
	for _, k := range portKeys {
		if k == "intelligence" && !cfg.IntelligenceEnabled {
			continue
		}
		if other, dup := seen[ports[k]]; dup {
			errs.add("port collision: ports.%s and ports.%s are both %d", other, k, ports[k])
		}
		seen[ports[k]] = k
	}
	cfg.Ports = Ports{
		Web: ports["web"], Core: ports["core"], Postgres: ports["postgres"], Redis: ports["redis"],
		MinioAPI: ports["minio_api"], MinioConsole: ports["minio_console"],
	}
	if cfg.IntelligenceEnabled {
		cfg.Ports.Intelligence = ports["intelligence"]
	}

	// ai_models
	if rawModels, ok := fields["ai_models"]; ok {
		if intelligenceKnown && !cfg.IntelligenceEnabled {
			errs.add("ai_models is set but intelligence is disabled; disabled intelligence takes no model configuration")
		} else {
			cfg.AIModels = parseModels(rawModels, &errs)
		}
	}

	if len(errs) > 0 {
		return Config{}, errs
	}
	return cfg, nil
}

func parseModels(raw json.RawMessage, errs *ValidationErrors) []ModelSetting {
	var given map[string]json.RawMessage
	if err := json.Unmarshal(raw, &given); err != nil {
		errs.add("field %q must be an object", "ai_models")
		return nil
	}
	allowed := map[string]bool{}
	for _, c := range Capabilities {
		allowed[c] = true
		allowed[c+"_fallbacks"] = true
	}
	keys := make([]string, 0, len(given))
	for k := range given {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	values := map[string]string{}
	for _, k := range keys {
		if !allowed[k] {
			errs.add("unknown ai_models key %q (capabilities: %s, each optionally with _fallbacks)", k, strings.Join(Capabilities, ", "))
			continue
		}
		var s string
		if err := json.Unmarshal(given[k], &s); err != nil {
			errs.add("ai_models.%s must be a string", k)
			continue
		}
		values[k] = s
	}
	var out []ModelSetting
	for _, c := range Capabilities {
		primary, hasPrimary := values[c]
		fb, hasFallbacks := values[c+"_fallbacks"]
		if hasFallbacks && !hasPrimary {
			errs.add("ai_models.%s_fallbacks is set without ai_models.%s", c, c)
			continue
		}
		if !hasPrimary {
			continue
		}
		if msg := CheckModelIdentity(primary); msg != "" {
			errs.add("ai_models.%s %q %s", c, primary, msg)
			continue
		}
		setting := ModelSetting{Capability: c, Primary: primary}
		if hasFallbacks {
			used := map[string]bool{primary: true}
			for _, f := range strings.Split(fb, ",") {
				if msg := CheckModelIdentity(f); msg != "" {
					errs.add("ai_models.%s_fallbacks entry %q %s", c, f, msg)
					continue
				}
				if used[f] {
					errs.add("ai_models.%s_fallbacks repeats %q", c, f)
					continue
				}
				used[f] = true
				setting.Fallbacks = append(setting.Fallbacks, f)
			}
		}
		out = append(out, setting)
	}
	return out
}

// CheckModelIdentity validates a canonical provider:model identity. The
// identity is split on the first colon; the model part may contain colons.
func CheckModelIdentity(id string) string {
	provider, model, ok := strings.Cut(id, ":")
	switch {
	case !ok:
		return "must have the form provider:model"
	case !providerRe.MatchString(provider):
		return fmt.Sprintf("has an invalid provider %q", provider)
	case gatewayProviders[provider]:
		return fmt.Sprintf("names the gateway %q as its provider; name the model provider instead (the gateway is not a domain concept)", provider)
	case model == "":
		return "has an empty model"
	case !modelRe.MatchString(model):
		return fmt.Sprintf("has an invalid model %q", model)
	}
	return ""
}

func checkProductName(name string) string {
	n := utf8.RuneCountInString(name)
	if n < 1 || n > 64 {
		return "must be 1-64 characters"
	}
	if strings.TrimSpace(name) != name {
		return "must not have leading or trailing whitespace"
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return "must not contain control or non-printable characters"
		}
		if strings.ContainsRune("\"\\$`", r) {
			return fmt.Sprintf("must not contain %q (env and Compose interpolation safety)", r)
		}
	}
	return ""
}

func checkHostname(h string) string {
	if h == "" {
		return "must not be empty"
	}
	if strings.ToLower(h) != h {
		return "must be lowercase"
	}
	if strings.ContainsAny(h, ":/*@ ") {
		return "must be a bare hostname (no scheme, port, path or wildcard)"
	}
	if len(h) > 253 {
		return "is longer than 253 characters"
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return "must have at least two labels"
	}
	for _, l := range labels {
		if !labelRe.MatchString(l) {
			return fmt.Sprintf("has an invalid label %q", l)
		}
	}
	return ""
}

// DeriveSlug converts a product name into a kebab-case slug.
func DeriveSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}
