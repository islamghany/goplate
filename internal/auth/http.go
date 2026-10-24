package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/identity"
	"github.com/islamghany/goplate/internal/ratelimit"
	"github.com/islamghany/goplate/internal/web"
)

// TransportConfig decides how tokens travel (ADR-0008).
type TransportConfig struct {
	// BearerEnabled accepts "Authorization: Bearer <token>" and allows
	// clients to receive tokens in response bodies (mobile, CLIs, services).
	BearerEnabled bool
	// CookieEnabled delivers tokens as HttpOnly cookies (browsers).
	CookieEnabled bool
	// CookieSecure must be true everywhere except plain-HTTP local dev.
	CookieSecure bool
	// CookieDomain is optional; empty means host-only cookies (safest).
	CookieDomain string
	// RefreshPath is the only path the refresh cookie is sent to.
	RefreshPath string
}

// cookieNames picks the most restrictive cookie prefixes available:
// "__Host-" requires Secure, Path=/ and no Domain; "__Secure-" requires
// Secure. Browsers enforce these, so a subdomain cannot overwrite them.
func cookieNames(c TransportConfig) (access, refresh string) {
	switch {
	case !c.CookieSecure:
		return "access_token", "refresh_token"
	case c.CookieDomain == "":
		return "__Host-access_token", "__Secure-refresh_token"
	default:
		return "__Secure-access_token", "__Secure-refresh_token"
	}
}

// LoginThrottle limits login attempts per email address, on top of the
// per-IP limit applied by middleware, to slow down credential stuffing
// spread across many IPs.
type LoginThrottle struct {
	Limiter    ratelimit.Limiter
	Policy     ratelimit.Policy
	FailClosed bool
}

// Handler exposes authentication over HTTP and provides the middleware
// other modules' routes use.
type Handler struct {
	svc           *Service
	cfg           TransportConfig
	throttle      *LoginThrottle
	log           *slog.Logger
	accessCookie  string
	refreshCookie string
}

// NewHandler returns a Handler. throttle may be nil.
func NewHandler(svc *Service, cfg TransportConfig, throttle *LoginThrottle, log *slog.Logger) *Handler {
	a, r := cookieNames(cfg)
	if cfg.RefreshPath == "" {
		cfg.RefreshPath = "/"
	}
	return &Handler{svc: svc, cfg: cfg, throttle: throttle, log: log, accessCookie: a, refreshCookie: r}
}

// Routes registers the auth endpoints. login is the router for the login
// endpoint (it carries a stricter rate limit).
func (h *Handler) Routes(public, login, protected *web.Router) {
	login.Handle("POST /auth/login", h.login)
	public.Handle("POST /auth/refresh", h.refresh)
	protected.Handle("POST /auth/logout", h.logout)
	protected.Handle("POST /auth/logout-all", h.logoutAll)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	// TokenDelivery is "cookie" (default when cookies are enabled) or
	// "body". Browser apps should never ask for "body": tokens readable by
	// JavaScript can be stolen through any XSS bug.
	TokenDelivery string `json:"token_delivery"`
}

type tokenResponse struct {
	UserID           uuid.UUID `json:"user_id"`
	TokenType        string    `json:"token_type,omitempty"`
	AccessToken      string    `json:"access_token,omitempty"`
	ExpiresAt        time.Time `json:"expires_at"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) error {
	req, err := web.Decode[loginRequest](r)
	if err != nil {
		return err
	}
	inBody, err := h.deliverInBody(req.TokenDelivery)
	if err != nil {
		return err
	}
	if err := h.throttleEmail(w, r, req.Email); err != nil {
		return err
	}
	t, err := h.svc.Login(r.Context(), req.Email, req.Password, clientMeta(r))
	if err != nil {
		return err
	}
	return h.writeTokens(w, t, inBody)
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// refresh accepts the refresh token from its cookie, or from the JSON body
// for Bearer clients, and answers the same way it was asked.
func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) error {
	var token string
	inBody := false
	if c, err := r.Cookie(h.refreshCookie); h.cfg.CookieEnabled && err == nil {
		token = c.Value
	} else if h.cfg.BearerEnabled {
		req, err := web.Decode[refreshRequest](r)
		if err != nil {
			return err
		}
		token, inBody = req.RefreshToken, true
	}
	t, err := h.svc.Refresh(r.Context(), token, clientMeta(r))
	if err != nil {
		h.clearCookies(w)
		return err
	}
	return h.writeTokens(w, t, inBody)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.Logout(r.Context()); err != nil {
		return err
	}
	h.clearCookies(w)
	return web.NoContent(w)
}

func (h *Handler) logoutAll(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.LogoutAll(r.Context()); err != nil {
		return err
	}
	h.clearCookies(w)
	return web.NoContent(w)
}

// Authenticate is the middleware protecting routes. If an Authorization
// header is present it is the only credential considered; otherwise the
// access cookie is used.
func (h *Handler) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := h.accessToken(r)
		if token == "" {
			web.WriteError(h.log, w, r, identity.ErrUnauthenticated)
			return
		}
		p, err := h.svc.Authenticate(r.Context(), token)
		if err != nil {
			web.WriteError(h.log, w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(identity.WithPrincipal(r.Context(), p)))
	})
}

func (h *Handler) accessToken(r *http.Request) string {
	if authz := r.Header.Get("Authorization"); authz != "" {
		scheme, token, ok := strings.Cut(authz, " ")
		if !h.cfg.BearerEnabled || !ok || !strings.EqualFold(scheme, "Bearer") {
			return ""
		}
		return strings.TrimSpace(token)
	}
	if h.cfg.CookieEnabled {
		if c, err := r.Cookie(h.accessCookie); err == nil {
			return c.Value
		}
	}
	return ""
}

// RequireRole rejects callers without one of roles. Use it for coarse
// route-level gates; services still perform fine-grained checks.
func RequireRole(log *slog.Logger, roles ...identity.Role) web.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := identity.FromContext(r.Context())
			if !ok {
				web.WriteError(log, w, r, identity.ErrUnauthenticated)
				return
			}
			for _, role := range roles {
				if p.Role == role {
					next.ServeHTTP(w, r)
					return
				}
			}
			web.WriteError(log, w, r, errs.New(errs.PermissionDenied, "insufficient role"))
		})
	}
}

func (h *Handler) deliverInBody(mode string) (bool, error) {
	switch mode {
	case "", "cookie":
		if h.cfg.CookieEnabled {
			return false, nil
		}
		return true, nil
	case "body":
		if !h.cfg.BearerEnabled {
			return false, errs.Validation(errs.FieldError{Field: "token_delivery", Message: "body delivery is disabled"})
		}
		return true, nil
	default:
		return false, errs.Validation(errs.FieldError{Field: "token_delivery", Message: `must be "cookie" or "body"`})
	}
}

func (h *Handler) writeTokens(w http.ResponseWriter, t Tokens, inBody bool) error {
	resp := tokenResponse{UserID: t.UserID, ExpiresAt: t.AccessExpiresAt, RefreshExpiresAt: t.RefreshExpiresAt}
	if inBody {
		resp.TokenType, resp.AccessToken, resp.RefreshToken = "Bearer", t.AccessToken, t.RefreshToken
	} else {
		h.setCookie(w, h.accessCookie, t.AccessToken, "/", t.AccessExpiresAt)
		h.setCookie(w, h.refreshCookie, t.RefreshToken, h.cfg.RefreshPath, t.RefreshExpiresAt)
	}
	w.Header().Set("Cache-Control", "no-store") // never cache responses carrying tokens
	return web.JSON(w, http.StatusOK, resp)
}

func (h *Handler) setCookie(w http.ResponseWriter, name, value, path string, expires time.Time) {
	//nolint:gosec // Secure is false only when APP_ENV=development (plain-HTTP localhost).
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		Domain:   h.cfg.CookieDomain,
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		Secure:   h.cfg.CookieSecure,
		HttpOnly: true,                 // invisible to JavaScript
		SameSite: http.SameSiteLaxMode, // not sent on cross-site POSTs
	})
}

func (h *Handler) clearCookies(w http.ResponseWriter) {
	if !h.cfg.CookieEnabled {
		return
	}
	for _, c := range []struct{ name, path string }{{h.accessCookie, "/"}, {h.refreshCookie, h.cfg.RefreshPath}} {
		//nolint:gosec // see setCookie
		http.SetCookie(w, &http.Cookie{Name: c.name, Value: "", Path: c.path, Domain: h.cfg.CookieDomain,
			MaxAge: -1, Secure: h.cfg.CookieSecure, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	}
}

func (h *Handler) throttleEmail(w http.ResponseWriter, r *http.Request, email string) error {
	if h.throttle == nil || email == "" {
		return nil
	}
	// Hash the address so raw emails never end up as Redis keys.
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	d, err := h.throttle.Limiter.Allow(r.Context(), h.throttle.Policy.Name+":email:"+hex.EncodeToString(sum[:16]), h.throttle.Policy)
	switch {
	case err != nil && h.throttle.FailClosed:
		return errs.Wrap(errs.Unavailable, "", err)
	case err != nil:
		h.log.WarnContext(r.Context(), "login throttle unavailable", "err", err)
		return nil
	case !d.Allowed:
		w.Header().Set("Retry-After", strconv.Itoa(int(d.RetryAfter.Seconds())+1))
		return ratelimit.ErrLimited
	}
	return nil
}

func clientMeta(r *http.Request) ClientMeta {
	return ClientMeta{UserAgent: r.UserAgent(), IP: web.ClientIP(r)}
}
