// Package authpg is the Postgres adapter for the auth module's SessionStore.
// Generated sqlc code lives next to this file and never leaves the package.
package authpg

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/auth"
	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/platform/postgres"
)

// SessionStore implements auth.SessionStore.
type SessionStore struct {
	txm *postgres.TxManager
}

// NewSessionStore returns a SessionStore.
func NewSessionStore(txm *postgres.TxManager) *SessionStore {
	return &SessionStore{txm: txm}
}

var _ auth.SessionStore = (*SessionStore)(nil)

func (s *SessionStore) q(ctx context.Context) *Queries { return New(s.txm.DB(ctx)) }

func (s *SessionStore) Create(ctx context.Context, sess auth.Session) error {
	return wrap(s.q(ctx).InsertSession(ctx, InsertSessionParams{
		ID:        sess.ID,
		FamilyID:  sess.FamilyID,
		UserID:    sess.UserID,
		TokenHash: sess.TokenHash,
		ExpiresAt: sess.ExpiresAt,
		UserAgent: sess.UserAgent,
		Ip:        sess.IP,
		CreatedAt: sess.CreatedAt,
	}))
}

func (s *SessionStore) ByTokenHashForUpdate(ctx context.Context, hash []byte) (auth.Session, error) {
	row, err := s.q(ctx).GetSessionByTokenHashForUpdate(ctx, hash)
	switch {
	case postgres.IsNoRows(err):
		return auth.Session{}, auth.ErrSessionNotFound
	case err != nil:
		return auth.Session{}, errs.Wrap(errs.Internal, "", err)
	}
	return auth.Session{
		ID:        row.ID,
		FamilyID:  row.FamilyID,
		UserID:    row.UserID,
		TokenHash: row.TokenHash,
		ExpiresAt: row.ExpiresAt,
		UsedAt:    row.UsedAt,
		RevokedAt: row.RevokedAt,
		UserAgent: row.UserAgent,
		IP:        row.Ip,
		CreatedAt: row.CreatedAt,
	}, nil
}

func (s *SessionStore) MarkUsed(ctx context.Context, id uuid.UUID, at time.Time) error {
	return wrap(s.q(ctx).MarkSessionUsed(ctx, MarkSessionUsedParams{ID: id, UsedAt: &at}))
}

func (s *SessionStore) RevokeFamily(ctx context.Context, familyID uuid.UUID, at time.Time) error {
	return wrap(s.q(ctx).RevokeFamily(ctx, RevokeFamilyParams{FamilyID: familyID, RevokedAt: &at}))
}

func (s *SessionStore) RevokeFamilyOf(ctx context.Context, sessionID uuid.UUID, at time.Time) error {
	return wrap(s.q(ctx).RevokeFamilyOfSession(ctx, RevokeFamilyOfSessionParams{SessionID: sessionID, RevokedAt: &at}))
}

func (s *SessionStore) RevokeUser(ctx context.Context, userID uuid.UUID, at time.Time) error {
	return wrap(s.q(ctx).RevokeUserSessions(ctx, RevokeUserSessionsParams{UserID: userID, RevokedAt: &at}))
}

func (s *SessionStore) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	n, err := s.q(ctx).DeleteExpiredSessions(ctx, before)
	return n, wrap(err)
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	return errs.Wrap(errs.Internal, "", err)
}
