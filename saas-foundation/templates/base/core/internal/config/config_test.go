package config

import (
	"strings"
	"testing"
)

func baseEnv() map[string]string {
	return map[string]string{
		"PRODUCT_NAME":                  "Sample Product",
		"PLATFORM_BASE_DOMAIN":          "sample.example.test",
		"CUSTOM_DOMAIN_CNAME_TARGET":    "custom.sample.example.test",
		"DATABASE_NAME":                 "sample_app",
		"APP_ENV":                       "test",
		"DB_HOST":                       "postgres",
		"DB_PORT":                       "5432",
		"DB_USER":                       "sample_app_runtime",
		"DB_PASSWORD":                   "db-secret-value",
		"REDIS_HOST":                    "redis",
		"REDIS_PORT":                    "6379",
		"REDIS_PASSWORD":                "redis-secret-value",
		"OBJECT_STORE_ENDPOINT":         "http://minio:9000",
		"OBJECT_STORE_REGION":           "us-east-1",
		"OBJECT_STORE_BUCKET":           "sample-objects",
		"OBJECT_STORE_FORCE_PATH_STYLE": "true",
		"OBJECT_STORE_ACCESS_KEY":       "core-service",
		"OBJECT_STORE_SECRET_KEY":       "object-secret-value",
	}
}

func load(env map[string]string) (Config, error) {
	get := func(k string) string { return env[k] }
	return Load(get, EnvSecrets{Getenv: get})
}

func TestLoadValid(t *testing.T) {
	c, err := load(baseEnv())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.ProductName != "Sample Product" || c.DB.Name != "sample_app" || !c.ObjectStore.ForcePathStyle {
		t.Fatalf("unexpected config: %+v", c)
	}
	if c.Intelligence.Enabled() {
		t.Fatal("intelligence must be disabled when INTELLIGENCE_URL is unset")
	}
	if got := len(c.SecretValues()); got != 3 {
		t.Fatalf("expected 3 secret values, got %d", got)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	env := baseEnv()
	delete(env, "PRODUCT_NAME")
	delete(env, "DB_PASSWORD")
	env["APP_ENV"] = "staging"
	env["PLATFORM_BASE_DOMAIN"] = "Not-Lowercase.Example"
	_, err := load(env)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"PRODUCT_NAME is required", "DB_PASSWORD is not set", "APP_ENV must be", "PLATFORM_BASE_DOMAIN must be"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestLoadErrorNeverContainsSecrets(t *testing.T) {
	env := baseEnv()
	delete(env, "PRODUCT_NAME")
	_, err := load(env)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, secret := range []string{"db-secret-value", "redis-secret-value", "object-secret-value"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks a secret: %v", err)
		}
	}
}

func TestIntelligenceRequiresTokenWhenEnabled(t *testing.T) {
	env := baseEnv()
	env["INTELLIGENCE_URL"] = "http://intelligence:8000"
	if _, err := load(env); err == nil || !strings.Contains(err.Error(), "INTELLIGENCE_SERVICE_TOKEN") {
		t.Fatalf("expected missing token error, got %v", err)
	}
	env["INTELLIGENCE_SERVICE_TOKEN"] = "token-value"
	c, err := load(env)
	if err != nil || !c.Intelligence.Enabled() {
		t.Fatalf("expected intelligence enabled, err=%v", err)
	}
}

func TestTokenWithoutURLIsRejected(t *testing.T) {
	env := baseEnv()
	env["INTELLIGENCE_SERVICE_TOKEN"] = "token-value"
	if _, err := load(env); err == nil {
		t.Fatal("expected error when a token is set without INTELLIGENCE_URL")
	}
}
