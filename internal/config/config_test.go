package config_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/islamghany/goplate/internal/config"
)

func minimal() map[string]string {
	return map[string]string{
		"POSTGRES_URL":    "postgres://u:p@localhost/db",
		"REDIS_URL":       "redis://localhost:6379/0",
		"AUTH_JWT_SECRET": strings.Repeat("s", 32),
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := config.Load(minimal())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Addr != ":8080" || cfg.Auth.AccessTTL != 15*time.Minute || cfg.App.Env != config.EnvProduction {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.HTTP.DocsEnabled {
		t.Fatal("docs must be off by default (production-safe defaults)")
	}
}

func TestValidationListsEveryProblem(t *testing.T) {
	env := minimal()
	env["AUTH_JWT_SECRET"] = "short"
	env["CACHE_DRIVER"] = "memcached"
	env["HTTP_TRUSTED_ORIGINS"] = "*"
	delete(env, "REDIS_URL")

	_, err := config.Load(env)
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"AUTH_JWT_SECRET", "CACHE_DRIVER", "HTTP_TRUSTED_ORIGINS", "REDIS_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}

func TestRedisOptionalWithMemoryDrivers(t *testing.T) {
	env := minimal()
	delete(env, "REDIS_URL")
	env["CACHE_DRIVER"], env["RATELIMIT_DRIVER"] = "memory", "memory"
	if _, err := config.Load(env); err != nil {
		t.Fatal(err)
	}
}

func TestParsesListsAndCIDRs(t *testing.T) {
	env := minimal()
	env["HTTP_TRUSTED_PROXIES"] = "10.0.0.0/8,192.168.0.0/16"
	env["HTTP_TRUSTED_ORIGINS"] = "https://a.example,https://b.example"
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.HTTP.TrustedProxies) != 2 || cfg.HTTP.TrustedProxies[1].String() != "192.168.0.0/16" || len(cfg.HTTP.TrustedOrigins) != 2 {
		t.Fatalf("got %+v", cfg.HTTP)
	}
}

func TestSecretsNeverPrint(t *testing.T) {
	cfg, _ := config.Load(minimal())
	b, _ := json.Marshal(cfg)
	for _, out := range []string{fmt.Sprint(cfg.Auth), fmt.Sprintf("%+v", cfg.Postgres), fmt.Sprintf("%#v", cfg.Auth.JWTSecret), string(b)} {
		if strings.Contains(out, "sssss") || strings.Contains(out, "u:p@") {
			t.Fatalf("secret leaked: %s", out)
		}
	}
	if cfg.Auth.JWTSecret.Value() != strings.Repeat("s", 32) {
		t.Fatal("Value must return the real secret")
	}
}
