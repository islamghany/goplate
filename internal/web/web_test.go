package web_test

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/validate"
	"github.com/islamghany/goplate/internal/web"
)

var discard = slog.New(slog.DiscardHandler)

// stack mirrors the production middleware order closely enough for tests.
func stack(h http.Handler) http.Handler {
	return web.Chain(h,
		web.RequestIDMiddleware(),
		web.AccessLog(discard),
		web.Recover(discard),
		web.MaxBytes(64),
	)
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) web.Problem {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
	var p web.Problem
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRouterGroupsAndRegistry(t *testing.T) {
	var order []string
	mw := func(name string) web.Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	r := web.NewRouter(discard)
	v1 := r.Group("/v1", mw("group"))
	v1.Handle("GET /things/{id}", func(w http.ResponseWriter, r *http.Request) error {
		return web.JSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id"), "route": web.RoutePattern(r.Context())})
	}, mw("route"))

	rec := httptest.NewRecorder()
	stack(r).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/things/42", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body["id"] != "42" || body["route"] != "GET /v1/things/{id}" {
		t.Fatalf("body = %v", body)
	}
	if strings.Join(order, ",") != "group,route" {
		t.Fatalf("middleware order = %v", order)
	}
	if routes := r.Routes(); len(routes) != 1 || routes[0].String() != "GET /v1/things/{id}" {
		t.Fatalf("registry = %v", routes)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing X-Request-ID")
	}
}

func TestErrorsAreRenderedAsProblems(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantDetail string
	}{
		{"not found", errs.New(errs.NotFound, "note not found"), 404, "note not found"},
		{"conflict", errs.New(errs.Conflict, "email taken"), 409, "email taken"},
		{"internal hides details", errs.Wrap(errs.Internal, "", errors.New("pq: password=hunter2")), 500, "an unexpected error occurred; quote the request_id when reporting it"},
		{"plain errors are internal", errors.New("dial tcp 10.0.0.5:5432"), 500, "an unexpected error occurred; quote the request_id when reporting it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := web.NewRouter(discard)
			r.Handle("GET /x", func(http.ResponseWriter, *http.Request) error { return tt.err })
			rec := httptest.NewRecorder()
			stack(r).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			p := decodeProblem(t, rec)
			if p.Detail != tt.wantDetail {
				t.Fatalf("detail = %q, want %q", p.Detail, tt.wantDetail)
			}
			if p.RequestID == "" || p.RequestID != rec.Header().Get("X-Request-ID") {
				t.Fatal("problem must carry the request id")
			}
		})
	}
}

type createThing struct {
	Name string `json:"name"`
}

func (c createThing) Validate() error {
	v := validate.New()
	v.Required("name", c.Name)
	return v.Err()
}

func TestDecode(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
		wantField   string
	}{
		{"ok", `{"name":"a"}`, "application/json", 200, ""},
		{"empty body", ``, "application/json", 400, ""},
		{"malformed", `{"name":`, "application/json", 400, ""},
		{"unknown field", `{"nme":"a"}`, "application/json", 400, "nme"},
		{"wrong type", `{"name":1}`, "application/json", 400, "name"},
		{"two values", `{"name":"a"}{}`, "application/json", 400, ""},
		{"validation", `{"name":""}`, "application/json", 400, "name"},
		{"wrong content type", `{"name":"a"}`, "text/plain", 400, ""},
		{"too large", `{"name":"` + strings.Repeat("a", 100) + `"}`, "application/json", 400, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := web.NewRouter(discard)
			r.Handle("POST /things", func(w http.ResponseWriter, r *http.Request) error {
				in, err := web.Decode[createThing](r)
				if err != nil {
					return err
				}
				return web.JSON(w, http.StatusOK, in)
			})
			req := httptest.NewRequest(http.MethodPost, "/things", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			rec := httptest.NewRecorder()
			stack(r).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.wantField != "" {
				p := decodeProblem(t, rec)
				if len(p.Errors) != 1 || p.Errors[0].Field != tt.wantField {
					t.Fatalf("errors = %+v, want field %q", p.Errors, tt.wantField)
				}
			}
		})
	}
}

func TestRecoverTurnsPanicsInto500(t *testing.T) {
	r := web.NewRouter(discard)
	r.Handle("GET /panic", func(http.ResponseWriter, *http.Request) error { panic("boom") })
	rec := httptest.NewRecorder()
	stack(r).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if p := decodeProblem(t, rec); strings.Contains(p.Detail, "boom") {
		t.Fatal("panic value leaked to the client")
	}
}

func TestCORS(t *testing.T) {
	h := web.CORS(web.CORSConfig{
		AllowedOrigins:   []string{"https://app.example.com"},
		AllowedMethods:   []string{"GET", "POST"},
		AllowedHeaders:   []string{"Content-Type"},
		AllowCredentials: true,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))

	preflight := httptest.NewRequest(http.MethodOptions, "/", nil)
	preflight.Header.Set("Origin", "https://app.example.com")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, preflight)
	if rec.Code != http.StatusNoContent ||
		rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" ||
		rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("bad preflight: %d %v", rec.Code, rec.Header())
	}

	evil := httptest.NewRequest(http.MethodGet, "/", nil)
	evil.Header.Set("Origin", "https://evil.example.com")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, evil)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unknown origins must not be allowed")
	}
}

func TestRealIPTrustsOnlyConfiguredProxies(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	var got string
	h := web.Chain(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = web.ClientIP(r) }),
		web.RequestIDMiddleware(), web.RealIP(proxies))

	tests := []struct{ remote, xff, want string }{
		{"10.1.2.3:5000", "203.0.113.7, 10.0.0.9", "203.0.113.7"}, // via trusted proxies
		{"198.51.100.1:5000", "1.2.3.4", "198.51.100.1"},          // untrusted peer: header ignored
		{"10.1.2.3:5000", "", "10.1.2.3"},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = tt.remote
		if tt.xff != "" {
			req.Header.Set("X-Forwarded-For", tt.xff)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		if got != tt.want {
			t.Errorf("remote=%s xff=%q: got %s, want %s", tt.remote, tt.xff, got, tt.want)
		}
	}
}

func TestRequestIDIsReusedWhenWellFormed(t *testing.T) {
	h := stack(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, web.RequestID(r.Context()))
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "abc-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "abc-123" {
		t.Fatalf("got %q", rec.Body)
	}

	req.Header.Set("X-Request-ID", "<script>")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() == "<script>" || rec.Body.Len() == 0 {
		t.Fatalf("malformed ids must be replaced, got %q", rec.Body)
	}
}
