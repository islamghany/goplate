//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/islamghany/goplate/internal/testutil"
)

// The end-to-end test boots the real application (build + lifecycle) against
// real Postgres and Redis containers and drives it over HTTP, exercising the
// main flows across modules, middleware and adapters.

var baseURL string

func TestMain(m *testing.M) {
	ctx, cancel := context.WithCancel(context.Background())
	env, cleanup, err := testutil.Start(ctx, testutil.Options{Redis: true})
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	addr, admin := freeAddr(), freeAddr()
	baseURL = "http://" + addr
	app, err := build(ctx, io.Discard, map[string]string{
		"APP_ENV":                 "development", // plain-HTTP cookies
		"HTTP_ADDR":               addr,
		"ADMIN_ADDR":              admin,
		"HTTP_TRUSTED_ORIGINS":    "http://app.local",
		"HTTP_DOCS_ENABLED":       "true",
		"POSTGRES_URL":            env.PostgresURL,
		"REDIS_URL":               env.RedisURL,
		"AUTH_JWT_SECRET":         strings.Repeat("e2e-secret", 4),
		"AUTH_DENYLIST_ENABLED":   "true",
		"RATELIMIT_LOGIN_IP_RATE": "1000",
		"SHUTDOWN_DRAIN_DELAY":    "0s",
		"OTEL_TRACES_EXPORTER":    "none",
	})
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	code := 1
	if waitReady() {
		code = m.Run()
	}
	cancel()
	if err := <-done; err != nil {
		fmt.Fprintln(os.Stderr, "shutdown:", err)
		code = 1
	}
	cleanup()
	os.Exit(code)
}

func TestBearerFlow(t *testing.T) {
	c := &client{t: t}
	email := fmt.Sprintf("ada-%d@example.com", time.Now().UnixNano())

	c.do("POST", "/v1/users", map[string]any{"email": email, "name": "Ada", "password": "correct horse battery"}, nil).want(201)
	c.do("POST", "/v1/users", map[string]any{"email": email, "name": "Ada", "password": "correct horse battery"}, nil).want(409)

	var tok struct{ AccessToken, RefreshToken string }
	c.do("POST", "/v1/auth/login", map[string]any{"email": email, "password": "correct horse battery", "token_delivery": "body"}, nil).
		want(200).decode(&tok, "access_token", "refresh_token")
	c.do("POST", "/v1/auth/login", map[string]any{"email": email, "password": "wrong password!!"}, nil).want(401)
	c.bearer = tok.AccessToken

	c.do("GET", "/v1/users/me", nil, nil).want(200)

	// Notes: create, read with ETag, optimistic concurrency, list, delete.
	var n struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	res := c.do("POST", "/v1/notes", map[string]any{"title": "Groceries", "tags": []string{"Home"}}, nil).want(201)
	res.decodeInto(&n)
	etag := res.header("ETag")

	c.do("PATCH", "/v1/notes/"+n.ID, map[string]any{"title": "Groceries v2"}, map[string]string{"If-Match": etag}).want(200)
	c.do("PATCH", "/v1/notes/"+n.ID, map[string]any{"title": "lost update"}, map[string]string{"If-Match": etag}).want(412)
	c.do("GET", "/v1/notes?tag=home", nil, nil).want(200)
	c.do("GET", "/v1/notes/not-a-uuid", nil, nil).want(400)

	// Refresh rotation and reuse detection.
	var rotated struct{ AccessToken, RefreshToken string }
	c.do("POST", "/v1/auth/refresh", map[string]any{"refresh_token": tok.RefreshToken}, nil).
		want(200).decode(&rotated, "access_token", "refresh_token")
	c.do("POST", "/v1/auth/refresh", map[string]any{"refresh_token": tok.RefreshToken}, nil).want(401)     // reuse
	c.do("POST", "/v1/auth/refresh", map[string]any{"refresh_token": rotated.RefreshToken}, nil).want(401) // family revoked

	// Logout denylists the access token immediately.
	c.bearer = rotated.AccessToken
	c.do("DELETE", "/v1/notes/"+n.ID, nil, nil).want(204)
	c.do("POST", "/v1/auth/logout", nil, nil).want(204)
	c.do("GET", "/v1/users/me", nil, nil).want(401)
}

func TestCookieFlowAndCSRF(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, http: &http.Client{Jar: jar}}
	email := fmt.Sprintf("grace-%d@example.com", time.Now().UnixNano())

	c.do("POST", "/v1/users", map[string]any{"email": email, "name": "Grace", "password": "correct horse battery"}, nil).want(201)
	res := c.do("POST", "/v1/auth/login", map[string]any{"email": email, "password": "correct horse battery"}, nil).want(200)
	if strings.Contains(res.body, "access_token") {
		t.Fatal("cookie delivery must not expose tokens to JavaScript")
	}

	c.do("GET", "/v1/users/me", nil, nil).want(200)
	c.do("POST", "/v1/notes", map[string]any{"title": "same-site"}, map[string]string{"Sec-Fetch-Site": "same-origin"}).want(201)
	c.do("POST", "/v1/notes", map[string]any{"title": "trusted"}, map[string]string{"Origin": "http://app.local", "Sec-Fetch-Site": "cross-site"}).want(201)
	c.do("POST", "/v1/notes", map[string]any{"title": "csrf"}, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}).want(403)

	c.do("POST", "/v1/auth/refresh", nil, nil).want(200) // refresh cookie is sent only to this path
}

func TestOpsEndpoints(t *testing.T) {
	c := &client{t: t}
	c.do("GET", "/livez", nil, nil).want(200)
	c.do("GET", "/readyz", nil, nil).want(200)
	c.do("GET", "/openapi.yaml", nil, nil).want(200)
	res := c.do("GET", "/v1/nope", nil, nil).want(404)
	if res.header("X-Request-ID") == "" {
		t.Fatal("every response carries a request id")
	}
}

// --- tiny HTTP test client -------------------------------------------------

type client struct {
	t      *testing.T
	http   *http.Client
	bearer string
}

type response struct {
	t      *testing.T
	status int
	body   string
	hdr    http.Header
	req    string
}

func (c *client) do(method, path string, body any, headers map[string]string) *response {
	c.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(c.t.Context(), method, baseURL+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	hc := c.http
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return &response{t: c.t, status: res.StatusCode, body: string(b), hdr: res.Header, req: method + " " + path}
}

func (r *response) want(status int) *response {
	r.t.Helper()
	if r.status != status {
		r.t.Fatalf("%s: status %d, want %d\n%s", r.req, r.status, status, r.body)
	}
	return r
}

func (r *response) header(k string) string { return r.hdr.Get(k) }

func (r *response) decodeInto(v any) {
	r.t.Helper()
	if err := json.Unmarshal([]byte(r.body), v); err != nil {
		r.t.Fatalf("%s: decode: %v", r.req, err)
	}
}

// decode fills the AccessToken/RefreshToken fields of v from snake_case keys.
func (r *response) decode(v *struct{ AccessToken, RefreshToken string }, accessKey, refreshKey string) *response {
	r.t.Helper()
	var m map[string]any
	r.decodeInto(&m)
	v.AccessToken, _ = m[accessKey].(string)
	v.RefreshToken, _ = m[refreshKey].(string)
	if v.AccessToken == "" || v.RefreshToken == "" {
		r.t.Fatalf("%s: missing tokens in %s", r.req, r.body)
	}
	return r
}

func freeAddr() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func waitReady() bool {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get(baseURL + "/readyz")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "app did not become ready")
	return false
}
