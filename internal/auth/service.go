package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/identity"
	"github.com/islamghany/goplate/internal/validate"
)

// Config holds token lifetimes.
type Config struct {
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

// Tokens is the result of a login or refresh.
type Tokens struct {
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
	UserID           uuid.UUID
}

// ClientMeta describes the client, stored with the session for auditing
// ("where am I logged in?").
type ClientMeta struct {
	UserAgent string
	IP        string
}

// Service implements authentication use cases.
type Service struct {
	users     UserFinder
	sessions  SessionStore
	tokens    TokenIssuer
	hasher    PasswordHasher
	tx        Transactor
	denylist  Denylist // optional
	log       *slog.Logger
	cfg       Config
	now       func() time.Time
	dummyHash string
}

// NewService returns a Service. denylist may be nil.
func NewService(users UserFinder, sessions SessionStore, tokens TokenIssuer, hasher PasswordHasher,
	tx Transactor, denylist Denylist, log *slog.Logger, cfg Config) (*Service, error) {
	dummy, err := hasher.Hash("dummy password used to equalize login timing")
	if err != nil {
		return nil, fmt.Errorf("build dummy hash: %w", err)
	}
	return &Service{users: users, sessions: sessions, tokens: tokens, hasher: hasher, tx: tx,
		denylist: denylist, log: log, cfg: cfg, now: time.Now, dummyHash: dummy}, nil
}

// Login verifies credentials and starts a new session family.
func (s *Service) Login(ctx context.Context, email, password string, meta ClientMeta) (Tokens, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	v := validate.New()
	v.Required("email", email)
	v.Required("password", password)
	if err := v.Err(); err != nil {
		return Tokens{}, err
	}

	creds, err := s.users.CredentialsByEmail(ctx, email)
	switch {
	case errs.Is(err, errs.NotFound):
		// Spend the same time as a real check so response timing does not
		// reveal which emails are registered.
		_, _ = s.hasher.Compare(s.dummyHash, password)
		return Tokens{}, ErrInvalidCredentials
	case err != nil:
		return Tokens{}, err
	}

	ok, err := s.hasher.Compare(creds.PasswordHash, password)
	if err != nil {
		return Tokens{}, errs.Wrap(errs.Internal, "", err)
	}
	if !ok {
		return Tokens{}, ErrInvalidCredentials
	}
	return s.startSession(ctx, creds, uuid.Must(uuid.NewV7()), meta)
}

// Refresh rotates a refresh token: the presented token is marked used and a
// new one in the same family is issued. Presenting a token that was already
// rotated is treated as theft and revokes the entire family.
func (s *Service) Refresh(ctx context.Context, refreshToken string, meta ClientMeta) (Tokens, error) {
	if refreshToken == "" {
		return Tokens{}, ErrInvalidRefresh
	}
	hash := hashToken(refreshToken)

	var (
		tokens Tokens
		reused bool
	)
	err := s.tx.WithinTx(ctx, func(ctx context.Context) error {
		sess, err := s.sessions.ByTokenHashForUpdate(ctx, hash)
		switch {
		case errs.Is(err, errs.NotFound):
			return ErrInvalidRefresh
		case err != nil:
			return err
		}

		now := s.now()
		switch {
		case sess.RevokedAt != nil:
			return ErrInvalidRefresh
		case sess.UsedAt != nil:
			// Commit the revocation (return nil) and report failure after.
			reused = true
			return s.sessions.RevokeFamily(ctx, sess.FamilyID, now)
		case now.After(sess.ExpiresAt):
			return ErrInvalidRefresh
		}

		creds, err := s.users.CredentialsByID(ctx, sess.UserID)
		if errs.Is(err, errs.NotFound) {
			return ErrInvalidRefresh // user was deleted
		}
		if err != nil {
			return err
		}
		if err := s.sessions.MarkUsed(ctx, sess.ID, now); err != nil {
			return err
		}
		tokens, err = s.startSession(ctx, creds, sess.FamilyID, meta)
		return err
	})
	if reused {
		s.log.WarnContext(ctx, "refresh token reuse detected; session family revoked", "ip", meta.IP)
		return Tokens{}, ErrInvalidRefresh
	}
	return tokens, err
}

// Logout revokes the caller's session family (this device).
func (s *Service) Logout(ctx context.Context) error {
	p, err := identity.Require(ctx)
	if err != nil {
		return err
	}
	if err := s.sessions.RevokeFamilyOf(ctx, p.SessionID, s.now()); err != nil {
		return err
	}
	s.denyAccessToken(ctx, p)
	return nil
}

// LogoutAll revokes every session of the caller (all devices).
func (s *Service) LogoutAll(ctx context.Context) error {
	p, err := identity.Require(ctx)
	if err != nil {
		return err
	}
	if err := s.sessions.RevokeUser(ctx, p.UserID, s.now()); err != nil {
		return err
	}
	s.denyAccessToken(ctx, p)
	return nil
}

// Authenticate verifies an access token and returns its principal.
func (s *Service) Authenticate(ctx context.Context, accessToken string) (identity.Principal, error) {
	p, err := s.tokens.Verify(accessToken)
	if err != nil {
		return identity.Principal{}, err
	}
	if s.denylist != nil && p.TokenID != "" {
		revoked, err := s.denylist.IsRevoked(ctx, p.TokenID)
		if err != nil {
			// Fail open: the token is still cryptographically valid and
			// short-lived; refusing every request during a cache outage
			// would turn a degraded feature into a full outage.
			s.log.WarnContext(ctx, "access token denylist unavailable", "err", err)
		} else if revoked {
			return identity.Principal{}, errs.New(errs.Unauthenticated, "access token has been revoked")
		}
	}
	return p, nil
}

// PurgeExpiredSessions deletes expired sessions. It runs as a periodic job
// and is idempotent.
func (s *Service) PurgeExpiredSessions(ctx context.Context, _ PurgeExpiredSessions) error {
	n, err := s.sessions.DeleteExpired(ctx, s.now())
	if err != nil {
		return err
	}
	s.log.InfoContext(ctx, "purged expired sessions", "count", n)
	return nil
}

func (s *Service) startSession(ctx context.Context, creds Credentials, familyID uuid.UUID, meta ClientMeta) (Tokens, error) {
	now := s.now().UTC().Truncate(time.Microsecond)
	refresh := rand.Text() + rand.Text() // 260 random bits, cookie- and URL-safe
	sess := Session{
		ID:        uuid.Must(uuid.NewV7()),
		FamilyID:  familyID,
		UserID:    creds.UserID,
		TokenHash: hashToken(refresh),
		ExpiresAt: now.Add(s.cfg.RefreshTTL),
		UserAgent: truncate(meta.UserAgent, 512),
		IP:        meta.IP,
		CreatedAt: now,
	}
	if err := s.sessions.Create(ctx, sess); err != nil {
		return Tokens{}, err
	}

	access, exp, err := s.tokens.Issue(identity.Principal{
		UserID: creds.UserID, SessionID: sess.ID, Role: creds.Role,
	}, s.cfg.AccessTTL)
	if err != nil {
		return Tokens{}, errs.Wrap(errs.Internal, "", err)
	}
	return Tokens{
		AccessToken:      access,
		AccessExpiresAt:  exp,
		RefreshToken:     refresh,
		RefreshExpiresAt: sess.ExpiresAt,
		UserID:           creds.UserID,
	}, nil
}

func (s *Service) denyAccessToken(ctx context.Context, p identity.Principal) {
	if s.denylist == nil || p.TokenID == "" {
		return
	}
	if err := s.denylist.Revoke(ctx, p.TokenID, time.Unix(p.ExpiresAt, 0)); err != nil {
		s.log.WarnContext(ctx, "could not denylist access token", "err", err)
	}
}

// hashToken returns the value stored instead of the refresh token itself,
// so a database leak does not leak usable tokens. A fast hash is fine here:
// the input is 256 bits of randomness, not a guessable password.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
