package jwt

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/identity"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

func principal() identity.Principal {
	return identity.Principal{UserID: uuid.New(), SessionID: uuid.New(), Role: identity.RoleAdmin}
}

func TestRoundTrip(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	hs, _ := NewHS256(secret, "goplate", "api")
	ed, _ := NewEdDSA(priv, "goplate", "api")

	for name, s := range map[string]*Signer{"HS256": hs, "EdDSA": ed} {
		t.Run(name, func(t *testing.T) {
			p := principal()
			tok, exp, err := s.Issue(p, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.Verify(tok)
			if err != nil {
				t.Fatal(err)
			}
			if got.UserID != p.UserID || got.SessionID != p.SessionID || got.Role != p.Role || got.TokenID == "" || got.ExpiresAt != exp.Unix() {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestRejects(t *testing.T) {
	s, _ := NewHS256(secret, "goplate", "api")
	other, _ := NewHS256([]byte("ffffffffffffffffffffffffffffffff"), "goplate", "api")
	wrongAud, _ := NewHS256(secret, "goplate", "other-api")
	past, _ := NewHS256(secret, "goplate", "api")
	past.now = func() time.Time { return time.Now().Add(-time.Hour) }

	good, _, _ := s.Issue(principal(), time.Minute)
	forged, _, _ := other.Issue(principal(), time.Minute)
	otherAud, _, _ := wrongAud.Issue(principal(), time.Minute)
	expired, _, _ := past.Issue(principal(), time.Minute)

	// alg "none" must never be accepted, whatever the claims say.
	none, _ := gojwt.NewWithClaims(gojwt.SigningMethodNone, gojwt.MapClaims{
		"sub": uuid.NewString(), "iss": "goplate", "aud": "api", "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(gojwt.UnsafeAllowNoneSignatureType)

	for name, tok := range map[string]string{
		"wrong key":      forged,
		"wrong audience": otherAud,
		"expired":        expired,
		"alg none":       none,
		"tampered":       good[:len(good)-2] + "xx",
		"garbage":        strings.Repeat("a", 20),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Verify(tok); !errs.Is(err, errs.Unauthenticated) {
				t.Fatalf("Verify = %v, want Unauthenticated", err)
			}
		})
	}
}

func TestShortSecretRejected(t *testing.T) {
	if _, err := NewHS256([]byte("short"), "i", "a"); err == nil {
		t.Fatal("short HS256 secrets must be rejected")
	}
}
