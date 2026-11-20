package database

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/lib/pq"
)

type Session struct {
	Token     string
	UserID    int64
	Username  string
	FullName  string
	Role      string
	ExpiresAt time.Time
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (s *Store) CreateSession(ctx context.Context, userID int64) (string, time.Time, error) {
	token, err := newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().Add(24 * time.Hour)
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions (id,user_id,expires_at) VALUES ($1,$2,$3)`, token, userID, expires)
	return token, expires, err
}

func (s *Store) ResolveSession(ctx context.Context, token string) (Session, error) {
	var sess Session
	err := s.db.QueryRowContext(ctx, `SELECT s.id,s.user_id,u.username,u.full_name,u.role,s.expires_at
		FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.id=$1 AND s.expires_at > now()`, token).
		Scan(&sess.Token, &sess.UserID, &sess.Username, &sess.FullName, &sess.Role, &sess.ExpiresAt)
	if err != nil {
		return Session{}, err
	}
	return sess, nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=$1`, token)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	return err
}

func IsUniqueViolation(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505"
	}
	return false
}
