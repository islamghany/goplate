# ADR-0008: Accept tokens from both the Authorization header and HttpOnly cookies

- **Status:** Accepted
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** security, auth, http

## Context

The same API usually serves browsers (SPA or SSR) and non-browser clients
(mobile apps, CLIs, other services). They need tokens delivered differently:

- **Browsers**: tokens readable by JavaScript can be stolen by any XSS bug.
  HttpOnly cookies can't be read by scripts, but browsers send them
  automatically, which creates **CSRF** risk.
- **Non-browser clients**: cookies are awkward. A `Authorization: Bearer`
  header is the norm, and there's no CSRF risk because nothing is attached
  automatically.

Two configuration mistakes are common here: cookies with `SameSite=None`
but no `Secure` flag (modern browsers reject that combination), and CORS
that sends `Access-Control-Allow-Origin: *` together with
`Allow-Credentials: true`, which browsers also refuse. Neither provides any
CSRF protection.

## Decision drivers

1. Be safe by default in browsers (protect against both XSS token theft and CSRF).
2. Mobile and service clients shouldn't have to deal with cookies.
3. One authentication middleware, not two code paths per handler.

## Considered options

1. **Both, configurable**: Bearer header and cookies, each enabled by
   config, with CSRF protection for cookie-authenticated requests.
2. **Bearer only**: the SPA keeps the token in memory or `localStorage`.
3. **Cookies only**.

CSRF strategies for cookie mode:

- **a. Fetch Metadata / Origin check**: reject unsafe methods whose
  `Sec-Fetch-Site` or `Origin` isn't same-origin or on the trusted list.
- **b. Double-submit token**: a CSRF cookie plus a matching header.
- **c. Synchronizer token**: a CSRF token stored server-side.

### Option 1: Both

- ✅ Each client type uses its natural mechanism.
- ❌ Two inputs to the auth middleware; needs a precedence rule.

### Option 2: Bearer only

- ✅ Simple, no CSRF.
- ❌ Browser apps must store tokens where JavaScript can reach them, so an
  XSS bug becomes account takeover.

### Option 3: Cookies only

- ❌ Mobile and machine clients are second-class.

## Decision

We use **option 1** with CSRF strategy **a**:

- Config: `AUTH_BEARER_ENABLED=true`, `AUTH_COOKIE_ENABLED=true` (both
  default on).
- **Precedence**: if an `Authorization` header is present, it's the only
  credential considered and cookies are ignored. Otherwise the access cookie
  is used.
- **Cookies**:
  - The access cookie is named `__Host-access_token`, with `Secure`,
    `HttpOnly`, `SameSite=Lax`, and `Path=/`.
  - The refresh cookie is named `__Secure-refresh_token`, with
    `Path=/v1/auth/refresh`, so it isn't sent with any other request.
  - In local development over plain HTTP, the prefixes and `Secure` are
    disabled only when `APP_ENV=development`.
- **CSRF**: the standard library's `http.CrossOriginProtection` (Go 1.25+,
  see ADR-0020) rejects unsafe methods (`POST`, `PUT`, `PATCH`, `DELETE`)
  whose `Sec-Fetch-Site` or `Origin` shows a cross-origin browser request,
  unless the origin is in `HTTP_TRUSTED_ORIGINS`. Non-browser clients send
  neither header, so Bearer clients are unaffected.
- **Login and refresh responses**: tokens are always set as cookies when
  cookie mode is on. They're returned in the JSON body only when the client
  asks for it (`"token_delivery": "body"`), so browser apps never receive
  tokens their scripts could read.
- **CORS**: explicit allowed origins from config. If the list contains
  `*`, `Allow-Credentials` is never sent.

## Consequences

### Positive

- Browser sessions resist both XSS token theft and CSRF; mobile and
  service clients use plain Bearer tokens.
- Cookie settings are correct by construction.

### Negative / trade-offs we accept

- SPA and API on different sites (not just different subdomains) need
  `SameSite=None`, which is an explicit config change, and the trusted
  origins have to be maintained.

## Revisit if

- Browser privacy changes break third-party cookies for your deployment.
  Serve the API same-site with the frontend.
