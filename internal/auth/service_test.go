package auth_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/auth"
	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/identity"
)

// The service is tested with hand-written fakes of its own ports: no
// database, no mocking framework, and the tests read like the use cases.

type fakeUsers struct{ byEmail map[string]auth.Credentials }

func (f fakeUsers) CredentialsByEmail(_ context.Context, email string) (auth.Credentials, error) {
	if c, ok := f.byEmail[email]; ok {
		return c, nil
	}
	return auth.Credentials{}, errs.New(errs.NotFound, "user not found")
}

func (f fakeUsers) CredentialsByID(_ context.Context, id uuid.UUID) (auth.Credentials, error) {
	for _, c := range f.byEmail {
		if c.UserID == id {
			return c, nil
		}
	}
	return auth.Credentials{}, errs.New(errs.NotFound, "user not found")
}

type fakeSessions struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*auth.Session
}

func (f *fakeSessions) Create(_ context.Context, s auth.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[s.ID] = &s
	return nil
}

func (f *fakeSessions) ByTokenHashForUpdate(_ context.Context, hash []byte) (auth.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.rows {
		if bytes.Equal(s.TokenHash, hash) {
			return *s, nil
		}
	}
	return auth.Session{}, auth.ErrSessionNotFound
}

func (f *fakeSessions) MarkUsed(_ context.Context, id uuid.UUID, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[id].UsedAt = &at
	return nil
}

func (f *fakeSessions) revokeWhere(match func(*auth.Session) bool, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.rows {
		if match(s) && s.RevokedAt == nil {
			s.RevokedAt = &at
		}
	}
}

func (f *fakeSessions) RevokeFamily(_ context.Context, fam uuid.UUID, at time.Time) error {
	f.revokeWhere(func(s *auth.Session) bool { return s.FamilyID == fam }, at)
	return nil
}

func (f *fakeSessions) RevokeFamilyOf(ctx context.Context, id uuid.UUID, at time.Time) error {
	f.mu.Lock()
	fam := f.rows[id].FamilyID
	f.mu.Unlock()
	return f.RevokeFamily(ctx, fam, at)
}

func (f *fakeSessions) RevokeUser(_ context.Context, user uuid.UUID, at time.Time) error {
	f.revokeWhere(func(s *auth.Session) bool { return s.UserID == user }, at)
	return nil
}

func (f *fakeSessions) DeleteExpired(context.Context, time.Time) (int64, error) { return 0, nil }

// fakeTokens issues "tokens" that are just the session ID.
type fakeTokens struct{}

func (fakeTokens) Issue(p identity.Principal, ttl time.Duration) (string, time.Time, error) {
	return p.SessionID.String(), time.Now().Add(ttl), nil
}

func (fakeTokens) Verify(string) (identity.Principal, error) { return identity.Principal{}, nil }

// plainHasher "hashes" by prefixing, which keeps tests instant.
type plainHasher struct{}

func (plainHasher) Hash(p string) (string, error)     { return "h:" + p, nil }
func (plainHasher) Compare(h, p string) (bool, error) { return h == "h:"+p, nil }

type noTx struct{}

func (noTx) WithinTx(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

func setup(t *testing.T) (*auth.Service, *fakeSessions, auth.Credentials) {
	t.Helper()
	alice := auth.Credentials{UserID: uuid.New(), PasswordHash: "h:correct password", Role: identity.RoleUser}
	sessions := &fakeSessions{rows: map[uuid.UUID]*auth.Session{}}
	svc, err := auth.NewService(fakeUsers{byEmail: map[string]auth.Credentials{"alice@example.com": alice}},
		sessions, fakeTokens{}, plainHasher{}, noTx{}, nil, slog.New(slog.DiscardHandler),
		auth.Config{AccessTTL: time.Minute, RefreshTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return svc, sessions, alice
}

func TestLogin(t *testing.T) {
	svc, _, alice := setup(t)
	ctx := t.Context()

	tok, err := svc.Login(ctx, " Alice@Example.com ", "correct password", auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if tok.UserID != alice.UserID || tok.AccessToken == "" || tok.RefreshToken == "" {
		t.Fatalf("tokens = %+v", tok)
	}

	for name, tc := range map[string][2]string{
		"wrong password": {"alice@example.com", "nope"},
		"unknown email":  {"bob@example.com", "correct password"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.Login(ctx, tc[0], tc[1], auth.ClientMeta{})
			if !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("err = %v; both cases must return the same error", err)
			}
		})
	}
}

func TestRefreshRotates(t *testing.T) {
	svc, _, _ := setup(t)
	ctx := t.Context()
	first, _ := svc.Login(ctx, "alice@example.com", "correct password", auth.ClientMeta{})

	second, err := svc.Refresh(ctx, first.RefreshToken, auth.ClientMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh must rotate the token")
	}
	if _, err := svc.Refresh(ctx, second.RefreshToken, auth.ClientMeta{}); err != nil {
		t.Fatalf("the new token must work: %v", err)
	}
}

func TestRefreshReuseRevokesFamily(t *testing.T) {
	svc, sessions, _ := setup(t)
	ctx := t.Context()
	stolen, _ := svc.Login(ctx, "alice@example.com", "correct password", auth.ClientMeta{})
	legit, _ := svc.Refresh(ctx, stolen.RefreshToken, auth.ClientMeta{}) // the real user rotates

	// The attacker replays the old token: reuse detected.
	if _, err := svc.Refresh(ctx, stolen.RefreshToken, auth.ClientMeta{}); err == nil || !errors.Is(err, auth.ErrInvalidRefresh) {
		t.Fatalf("reuse: err = %v", err)
	}
	// ...which also kills the legitimate descendant, forcing a re-login.
	if _, err := svc.Refresh(ctx, legit.RefreshToken, auth.ClientMeta{}); err == nil || !errors.Is(err, auth.ErrInvalidRefresh) {
		t.Fatalf("family must be revoked, err = %v", err)
	}
	for _, s := range sessions.rows {
		if s.RevokedAt == nil {
			t.Fatalf("session %s not revoked", s.ID)
		}
	}
}

func TestLogoutRevokesOnlyThisFamily(t *testing.T) {
	svc, _, alice := setup(t)
	ctx := t.Context()
	laptop, _ := svc.Login(ctx, "alice@example.com", "correct password", auth.ClientMeta{})
	phone, _ := svc.Login(ctx, "alice@example.com", "correct password", auth.ClientMeta{})

	// fakeTokens uses the session ID as the access token.
	laptopCtx := identity.WithPrincipal(ctx, identity.Principal{UserID: alice.UserID, SessionID: uuid.MustParse(laptop.AccessToken)})
	if err := svc.Logout(laptopCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(ctx, laptop.RefreshToken, auth.ClientMeta{}); err == nil || !errors.Is(err, auth.ErrInvalidRefresh) {
		t.Fatal("laptop session must be revoked")
	}
	if _, err := svc.Refresh(ctx, phone.RefreshToken, auth.ClientMeta{}); err != nil {
		t.Fatalf("phone session must survive: %v", err)
	}

	if err := svc.LogoutAll(laptopCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(ctx, phone.RefreshToken, auth.ClientMeta{}); err == nil {
		t.Fatal("logout-all must revoke every session")
	}
}

func TestRefreshRejectsUnknownToken(t *testing.T) {
	svc, _, _ := setup(t)
	for _, tok := range []string{"", "not-a-token"} {
		if _, err := svc.Refresh(t.Context(), tok, auth.ClientMeta{}); err == nil || !errors.Is(err, auth.ErrInvalidRefresh) {
			t.Fatalf("Refresh(%q) = %v", tok, err)
		}
	}
}
