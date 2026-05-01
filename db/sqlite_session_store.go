package db

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// SQLiteSessionStore implements the scs.Store interface for SQLite.
type SQLiteSessionStore struct {
	db          *sql.DB
	stopCleanup chan struct{}
}

// NewSQLiteSessionStore creates a new session store backed by SQLite.
// It starts a background goroutine to clean up expired sessions.
func NewSQLiteSessionStore(db *sql.DB, cleanupInterval time.Duration) *SQLiteSessionStore {
	s := &SQLiteSessionStore{
		db:          db,
		stopCleanup: make(chan struct{}),
	}
	go s.startCleanup(cleanupInterval)
	return s
}

// Find returns the data for a given session token. If the session token is
// not found or is expired, the returned exists flag will be false.
func (s *SQLiteSessionStore) Find(token string) ([]byte, bool, error) {
	var data []byte
	err := s.db.QueryRowContext(context.Background(),
		`SELECT data FROM cd_sessions WHERE token = ? AND julianday(expiry) > julianday('now')`,
		token,
	).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// Commit adds or updates a session.
func (s *SQLiteSessionStore) Commit(token string, data []byte, expiry time.Time) error {
	_, err := s.db.ExecContext(context.Background(),
		`INSERT INTO cd_sessions (token, data, expiry) VALUES (?, ?, ?)
		 ON CONFLICT(token) DO UPDATE SET data = excluded.data, expiry = excluded.expiry`,
		token, data, expiry.UTC().Format(time.RFC3339),
	)
	return err
}

// Delete removes a session token and data.
func (s *SQLiteSessionStore) Delete(token string) error {
	_, err := s.db.ExecContext(context.Background(),
		`DELETE FROM cd_sessions WHERE token = ?`,
		token,
	)
	return err
}

func (s *SQLiteSessionStore) startCleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_, err := s.db.ExecContext(context.Background(),
				`DELETE FROM cd_sessions WHERE julianday(expiry) <= julianday('now')`,
			)
			if err != nil {
				log.Printf("SQLite session cleanup error: %v", err)
			}
		case <-s.stopCleanup:
			return
		}
	}
}

// StopCleanup stops the background cleanup goroutine.
func (s *SQLiteSessionStore) StopCleanup() {
	close(s.stopCleanup)
}
