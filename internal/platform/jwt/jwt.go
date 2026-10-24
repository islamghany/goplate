// Package jwt issues and verifies short-lived access tokens (ADR-0007).
//
// Two algorithms are supported behind the same type:
//
//   - HS256: one shared secret signs and verifies. Simple; use it when only
//     this service verifies its own tokens.
//   - EdDSA (Ed25519): a private key signs, the public key verifies. Use it
//     when other services must verify tokens without being able to mint them.
//
// Verification pins the algorithm and requires iss, aud and exp, closing the
// classic JWT pitfalls ("alg": "none", algorithm confusion, missing expiry).
package jwt

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/identity"
)

// Signer issues and verifies access tokens.
type Signer struct {
	method   gojwt.SigningMethod
	signKey  any
	verifKey any
	issuer   string
	audience string
	now      func() time.Time
}

// NewHS256 returns a Signer using a shared secret (at least 32 bytes).
func NewHS256(secret []byte, issuer, audience string) (*Signer, error) {
	if len(secret) < 32 {
		return nil, errors.New("jwt: HS256 secret must be at least 32 bytes")
	}
	return &Signer{method: gojwt.SigningMethodHS256, signKey: secret, verifKey: secret,
		issuer: issuer, audience: audience, now: time.Now}, nil
}

// NewEdDSA returns a Signer using an Ed25519 key pair.
func NewEdDSA(priv ed25519.PrivateKey, issuer, audience string) (*Signer, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("jwt: invalid Ed25519 private key")
	}
	return &Signer{method: gojwt.SigningMethodEdDSA, signKey: priv, verifKey: priv.Public(),
		issuer: issuer, audience: audience, now: time.Now}, nil
}

type claims struct {
	gojwt.RegisteredClaims
	SessionID string `json:"sid"`
	Role      string `json:"role"`
}

// Issue signs a token for p that expires after ttl. It returns the token
// and its expiry.
func (s *Signer) Issue(p identity.Principal, ttl time.Duration) (string, time.Time, error) {
	now := s.now()
	exp := now.Add(ttl)
	c := claims{
		RegisteredClaims: gojwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   p.UserID.String(),
			Audience:  gojwt.ClaimStrings{s.audience},
			ExpiresAt: gojwt.NewNumericDate(exp),
			IssuedAt:  gojwt.NewNumericDate(now),
			ID:        rand.Text(),
		},
		SessionID: p.SessionID.String(),
		Role:      string(p.Role),
	}
	tok, err := gojwt.NewWithClaims(s.method, c).SignedString(s.signKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return tok, exp, nil
}

// ErrInvalidToken is returned for any token that fails verification. The
// reason is wrapped for logs but never shown to clients.
var ErrInvalidToken = errs.New(errs.Unauthenticated, "invalid or expired access token")

// Verify parses and validates token and returns the Principal it carries.
func (s *Signer) Verify(token string) (identity.Principal, error) {
	var c claims
	_, err := gojwt.ParseWithClaims(token, &c,
		func(*gojwt.Token) (any, error) { return s.verifKey, nil },
		gojwt.WithValidMethods([]string{s.method.Alg()}),
		gojwt.WithIssuer(s.issuer),
		gojwt.WithAudience(s.audience),
		gojwt.WithExpirationRequired(),
		gojwt.WithIssuedAt(),
		gojwt.WithLeeway(30*time.Second),
		gojwt.WithTimeFunc(s.now),
	)
	if err != nil {
		return identity.Principal{}, errs.Wrap(errs.Unauthenticated, ErrInvalidToken.Msg, err)
	}

	userID, err1 := uuid.Parse(c.Subject)
	sessionID, err2 := uuid.Parse(c.SessionID)
	role := identity.Role(c.Role)
	if err1 != nil || err2 != nil || !role.Valid() {
		return identity.Principal{}, ErrInvalidToken
	}
	return identity.Principal{
		UserID:    userID,
		SessionID: sessionID,
		Role:      role,
		TokenID:   c.ID,
		ExpiresAt: c.ExpiresAt.Unix(),
	}, nil
}
