// Package config loads and validates core configuration from the
// environment. Product, domain and database identity are always read at
// runtime; nothing here embeds a product literal.
package config

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Getenv reads one environment variable.
type Getenv func(string) string

// SecretProvider is the boundary for secret material. The local
// implementation reads the environment; production deployments substitute a
// secrets-manager-backed implementation without touching callers.
type SecretProvider interface {
	Secret(name string) (string, error)
}

// EnvSecrets reads secrets from the environment.
type EnvSecrets struct{ Getenv Getenv }

// Secret returns the named secret or an error when it is unset.
func (e EnvSecrets) Secret(name string) (string, error) {
	v := e.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("secret %s is not set", name)
	}
	return v, nil
}

// DB holds PostgreSQL connection settings as separate components, so
// passwords never need URL escaping.
type DB struct {
	Host     string
	Port     int
	Name     string
	User     string
	Password string
}

// Redis holds Redis connection settings.
type Redis struct {
	Host     string
	Port     int
	Password string
}

// ObjectStore holds S3-compatible object store settings.
type ObjectStore struct {
	Endpoint       string
	Region         string
	Bucket         string
	ForcePathStyle bool
	AccessKey      string
	SecretKey      string
}

// Intelligence holds the optional internal intelligence service settings.
// It is zero when intelligence is disabled.
type Intelligence struct {
	URL   string
	Token string
}

// Enabled reports whether the intelligence service is configured.
func (i Intelligence) Enabled() bool { return i.URL != "" }

// Config is the validated core configuration.
type Config struct {
	ProductName             string
	PlatformBaseDomain      string
	CustomDomainCNAMETarget string
	DatabaseName            string
	AppEnv                  string
	ListenAddr              string
	DB                      DB
	Redis                   Redis
	ObjectStore             ObjectStore
	Intelligence            Intelligence
}

// SecretValues lists loaded secret values so log output can redact them.
func (c Config) SecretValues() []string {
	var out []string
	for _, v := range []string{c.DB.Password, c.Redis.Password, c.ObjectStore.SecretKey, c.Intelligence.Token} {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

var (
	hostLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	dbNameRe    = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
)

var appEnvs = map[string]bool{"development": true, "test": true, "production": true}

// Load reads and validates configuration. Every problem is reported at once
// and startup fails closed.
func Load(getenv Getenv, secrets SecretProvider) (Config, error) {
	var problems []string
	req := func(name string) string {
		v := strings.TrimSpace(getenv(name))
		if v == "" {
			problems = append(problems, name+" is required")
		}
		return v
	}
	secret := func(name string) string {
		v, err := secrets.Secret(name)
		if err != nil {
			problems = append(problems, err.Error())
		}
		return v
	}
	port := func(name string) int {
		raw := req(name)
		if raw == "" {
			return 0
		}
		p, err := strconv.Atoi(raw)
		if err != nil || p < 1 || p > 65535 {
			problems = append(problems, name+" must be a port number")
		}
		return p
	}

	c := Config{
		ProductName:             req("PRODUCT_NAME"),
		PlatformBaseDomain:      req("PLATFORM_BASE_DOMAIN"),
		CustomDomainCNAMETarget: req("CUSTOM_DOMAIN_CNAME_TARGET"),
		DatabaseName:            req("DATABASE_NAME"),
		AppEnv:                  req("APP_ENV"),
		ListenAddr:              getenv("CORE_LISTEN_ADDR"),
	}
	if c.ListenAddr == "" {
		c.ListenAddr = ":8180"
	}
	for _, h := range []struct{ name, value string }{
		{"PLATFORM_BASE_DOMAIN", c.PlatformBaseDomain},
		{"CUSTOM_DOMAIN_CNAME_TARGET", c.CustomDomainCNAMETarget},
	} {
		if h.value != "" && !validHostname(h.value) {
			problems = append(problems, h.name+" must be a lowercase hostname")
		}
	}
	if c.DatabaseName != "" && !dbNameRe.MatchString(c.DatabaseName) {
		problems = append(problems, "DATABASE_NAME is not a valid database name")
	}
	if c.AppEnv != "" && !appEnvs[c.AppEnv] {
		problems = append(problems, "APP_ENV must be development, test or production")
	}

	c.DB = DB{Host: req("DB_HOST"), Port: port("DB_PORT"), Name: c.DatabaseName, User: req("DB_USER"), Password: secret("DB_PASSWORD")}
	c.Redis = Redis{Host: req("REDIS_HOST"), Port: port("REDIS_PORT"), Password: secret("REDIS_PASSWORD")}

	pathStyle := strings.ToLower(req("OBJECT_STORE_FORCE_PATH_STYLE"))
	if pathStyle != "" && pathStyle != "true" && pathStyle != "false" {
		problems = append(problems, "OBJECT_STORE_FORCE_PATH_STYLE must be true or false")
	}
	c.ObjectStore = ObjectStore{
		Endpoint:       req("OBJECT_STORE_ENDPOINT"),
		Region:         req("OBJECT_STORE_REGION"),
		Bucket:         req("OBJECT_STORE_BUCKET"),
		ForcePathStyle: pathStyle == "true",
		AccessKey:      req("OBJECT_STORE_ACCESS_KEY"),
		SecretKey:      secret("OBJECT_STORE_SECRET_KEY"),
	}

	if url := strings.TrimSpace(getenv("INTELLIGENCE_URL")); url != "" {
		c.Intelligence = Intelligence{URL: url, Token: secret("INTELLIGENCE_SERVICE_TOKEN")}
	} else if getenv("INTELLIGENCE_SERVICE_TOKEN") != "" {
		problems = append(problems, "INTELLIGENCE_SERVICE_TOKEN is set but INTELLIGENCE_URL is not")
	}

	if len(problems) > 0 {
		return Config{}, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	return c, nil
}

func validHostname(h string) bool {
	if len(h) > 253 {
		return false
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !hostLabelRe.MatchString(l) {
			return false
		}
	}
	return true
}
