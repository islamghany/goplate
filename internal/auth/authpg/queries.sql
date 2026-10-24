-- name: InsertSession :exec
INSERT INTO auth_sessions (id, family_id, user_id, token_hash, expires_at, user_agent, ip, created_at)
VALUES (@id, @family_id, @user_id, @token_hash, @expires_at, @user_agent, @ip, @created_at);

-- FOR UPDATE serializes concurrent refreshes of the same token, so exactly
-- one of them wins and the others are treated as reuse.
-- name: GetSessionByTokenHashForUpdate :one
SELECT * FROM auth_sessions WHERE token_hash = @token_hash FOR UPDATE;

-- name: MarkSessionUsed :exec
UPDATE auth_sessions SET used_at = @used_at WHERE id = @id;

-- name: RevokeFamily :exec
UPDATE auth_sessions SET revoked_at = @revoked_at
WHERE family_id = @family_id AND revoked_at IS NULL;

-- name: RevokeFamilyOfSession :exec
UPDATE auth_sessions SET revoked_at = @revoked_at
WHERE family_id = (SELECT s.family_id FROM auth_sessions s WHERE s.id = @session_id)
  AND revoked_at IS NULL;

-- name: RevokeUserSessions :exec
UPDATE auth_sessions SET revoked_at = @revoked_at
WHERE user_id = @user_id AND revoked_at IS NULL;

-- name: DeleteExpiredSessions :execrows
DELETE FROM auth_sessions WHERE expires_at < @before;
