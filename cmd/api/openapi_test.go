package main

import (
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/islamghany/goplate/api"
	"github.com/islamghany/goplate/internal/auth"
	"github.com/islamghany/goplate/internal/config"
	"github.com/islamghany/goplate/internal/lifecycle"
	"github.com/islamghany/goplate/internal/note"
	"github.com/islamghany/goplate/internal/platform/memory"
	"github.com/islamghany/goplate/internal/platform/password"
	"github.com/islamghany/goplate/internal/user"
	"github.com/islamghany/goplate/internal/web"
)

// TestOpenAPIMatchesRoutes is the drift test from ADR-0014: every route the
// server registers must be documented, and every documented operation must
// exist. It builds the real router with inert dependencies (no database),
// which works because module constructors only store their ports.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData(api.Spec)
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	if err := doc.Validate(t.Context()); err != nil {
		t.Fatalf("invalid spec: %v", err)
	}

	documented := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			documented[method+" "+path] = true
		}
	}

	registered := map[string]bool{}
	for _, r := range routerForTest(t).Routes() {
		key := r.String()
		if key == "GET /openapi.yaml" || key == "GET /docs" {
			continue // the documentation endpoints themselves
		}
		registered[key] = true
	}

	if len(registered) < 10 {
		t.Fatalf("only %d routes registered; the test would pass vacuously", len(registered))
	}
	for _, key := range slices.Sorted(maps.Keys(registered)) {
		if !documented[key] {
			t.Errorf("route %s is not documented in api/openapi.yaml", key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(documented)) {
		if !registered[key] {
			t.Errorf("api/openapi.yaml documents %s, but no such route exists", key)
		}
	}
}

func routerForTest(t *testing.T) *web.Router {
	t.Helper()
	cfg, err := config.Load(map[string]string{
		"POSTGRES_URL":      "postgres://unused",
		"AUTH_JWT_SECRET":   strings.Repeat("s", 32),
		"CACHE_DRIVER":      "memory",
		"RATELIMIT_DRIVER":  "memory",
		"HTTP_DOCS_ENABLED": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	hasher := password.NewHasher(password.Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 8, KeyLen: 16})
	authn, err := auth.NewModule(auth.Deps{Hasher: hasher, Log: log}, auth.ModuleConfig{})
	if err != nil {
		t.Fatal(err)
	}
	m := modules{
		users: user.NewModule(user.Deps{}),
		auth:  authn,
		notes: note.NewModule(note.Deps{Cache: memory.NewCache(), Log: log}, note.Config{}),
	}
	return newRouter(cfg, log, lifecycle.New(log, lifecycle.Config{}), memory.NewLimiter(), m)
}
