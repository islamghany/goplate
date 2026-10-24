# ADR-0007: Authenticate with short-lived JWT access tokens and rotating opaque refresh tokens

- **Status:** Accepted (including the Postgres session store; see "Deviation" below)
- **Date:** 2026-10-24
- **Deciders:** Islam Mostafa
- **Tags:** security, auth

## Context

A common first design issues two JWTs (access and refresh) with the same
claims and stores the refresh JWT in Redis. It usually has gaps like these:

- A token `version` is incremented on login but never put in the JWT, so it
  has no effect.
- Logout deletes a cache key built from a context value that nothing sets,
  so it revokes nothing.
- Verification doesn't pin the signing algorithm or check `iss` and `aud`.
- There's no refresh endpoint, no rotation and no reuse detection, so a
  stolen refresh token stays valid for its whole lifetime.
- "User not found" is detected with `sql.ErrNoRows` while pgx returns
  `pgx.ErrNoRows`, and other database errors are silently ignored.

## Decision drivers

1. Revocation: logging out and "log out everywhere" must actually work.
2. Containing token theft: a stolen refresh token must be detectable.
3. Cheap per-request verification (no database round-trip for every request).
4. Every piece (signing, hashing, session storage) can be replaced
   (ADR-0002).
5. No dependency on an external identity provider for the default build.

## Considered options

1. **Stateful server sessions only**: opaque session ID, looked up on every
   request.
2. **JWT access + JWT refresh**.
3. **Short-lived JWT access token + opaque, rotating refresh token stored
   hashed on the server, with reuse detection.**
4. **PASETO** instead of JWT, with either refresh model.
5. **Delegate to an external IdP** (Keycloak, Zitadel, Auth0, Cognito).

### Option 1: Server sessions

- ✅ Instant revocation; simple.
- ❌ A store lookup on every request; harder to share across services.

### Option 2: JWT + JWT

- ✅ Stateless.
- ❌ A refresh token must be revocable, which makes it stateful anyway.
  Encoding it as a JWT then adds size and claim-parsing risk for no benefit.

### Option 3: JWT access + opaque rotating refresh

- ✅ Requests are verified without I/O (signature + `exp`/`iss`/`aud`).
- ✅ Refresh tokens are random 256-bit values, stored only as SHA-256
  hashes, so a database leak doesn't expose usable tokens.
- ✅ Each refresh rotates the token. If an already-used token is presented
  again, the whole session family is revoked (OAuth 2.0 Security BCP
  refresh-token rotation).
- ❌ An access token stays valid until it expires (we keep the TTL short),
  unless an optional `jti` denylist is enabled.

### Option 4: PASETO

- ✅ Fewer foot-guns than JWT (no algorithm negotiation).
- ❌ Much smaller ecosystem (gateways, debuggers, other languages). Pinning
  JWT algorithms removes the main risk anyway.

### Option 5: External IdP

- ✅ Best choice once you need SSO, MFA, or social login.
- ❌ Extra infrastructure or a vendor bill on day one. The ports below let a
  fork switch to one later.

## Decision

We use **option 3**. Its parts:

- **Access token**: JWT, default TTL **15 minutes**. Claims: `sub` (user ID),
  `sid` (session ID), `role`, `iss`, `aud`, `iat`, `exp`, `jti`.
  Verification **pins the algorithm** with `jwt.WithValidMethods`, and
  requires `iss`, `aud` and `exp`.
- **Signer port** (`auth.TokenSigner`). The default adapter is **HS256** with
  a secret of at least 32 bytes. An **EdDSA (Ed25519)** adapter is provided
  for when other services need to verify tokens without being able to mint
  them.
- **Refresh token**: 32 random bytes, base64url-encoded, default TTL **30
  days** (sliding). Stored as `sha256(token)` in a session record:
  `{id, family_id, user_id, token_hash, expires_at, revoked_at,
  replaced_by, user_agent, ip}`.
- **Rotation and reuse detection**: `POST /v1/auth/refresh` marks the
  current record as used and issues a new one in the same family. Presenting
  a token that was already used revokes the entire family and returns 401.
- **Logout** revokes the session family. **Log out everywhere** revokes all
  of the user's families.
- **Password hashing port** (`auth.PasswordHasher`). The default adapter is
  **argon2id**, following OWASP parameters. Unlike bcrypt, it has no 72-byte
  input limit. Login always runs a hash comparison, even when the email isn't
  found, so response timing doesn't reveal which emails exist.
- Login failures return one generic `invalid credentials` error, and the
  login route has a stricter rate limit (ADR-0013).
- The authentication middleware puts an `auth.Principal{UserID, SessionID,
  Role}` in the request context. Authorization (`auth.RequireRole` for route-level gates, ownership
  checks) happens in services, not in middleware alone.

### Deviation from the questionnaire (confirmed)

The question about core features described the auth option as "revocation
via Redis". This ADR proposes that **the session store port's default
adapter is Postgres**, for these reasons:

- Sessions are security state. Redis eviction (`maxmemory`) or a restart
  without persistence would silently log every user out, or worse, lose
  revocations.
- It's auditable ("where am I logged in?") and joins naturally with users.
- Refresh traffic is low (about one request per 15 minutes per active user),
  so Postgres handles it easily.

The `auth.SessionStore` port means a Redis adapter is a drop-in replacement.
Redis is still used for the optional access-token `jti` denylist, where losing
data only degrades revocation and doesn't break login.

## Consequences

### Positive

- Logout and refresh-token theft detection both actually work.
- Signing, hashing and session storage can each be replaced on their own.

### Negative / trade-offs we accept

- Up to 15 minutes of access after revocation unless the denylist is
  enabled.
- Clients must implement refresh handling (documented in the README).

## Revisit if

- You need SSO, MFA, or social login. Put an external IdP behind the same
  `Principal` and middleware.
- Other services must verify tokens. Switch the default signer to EdDSA and
  publish a JWKS endpoint.
