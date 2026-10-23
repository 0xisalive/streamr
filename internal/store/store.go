// Package store provides a small SQLite-backed cache for metadata and, most
// importantly, aligned/generated subtitles — so an expensive Whisper
// transcription or alignment is done ONCE per (release, language) and reused
// across sessions and restarts. It uses modernc.org/sqlite, a pure-Go driver
// (no cgo), so the single static binary still cross-compiles to FreeBSD.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

// Open opens (and migrates) the SQLite database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS subtitles (
    key          TEXT PRIMARY KEY,   -- hash of (release-id, lang, source-sub-hash)
    lang         TEXT NOT NULL,
    engine       TEXT NOT NULL,      -- which tier produced it (ffsubsync/alass/whisperx/whisper-gen)
    vtt_path     TEXT NOT NULL,      -- path to the cached WebVTT on disk
    confidence   REAL NOT NULL,
    created_at   INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS meta_cache (
    key          TEXT PRIMARY KEY,   -- e.g. "tmdb:search:matrix"
    payload      TEXT NOT NULL,      -- JSON blob
    created_at   INTEGER NOT NULL,
    ttl_seconds  INTEGER NOT NULL
);`)
	return err
}

// --- subtitle cache ---

type SubtitleEntry struct {
	Key        string
	Lang       string
	Engine     string
	VTTPath    string
	Confidence float64
	CreatedAt  time.Time
}

// GetSubtitle returns a cached subtitle for key, if present.
func (s *Store) GetSubtitle(ctx context.Context, key string) (*SubtitleEntry, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT key, lang, engine, vtt_path, confidence, created_at FROM subtitles WHERE key = ?`, key)
	var e SubtitleEntry
	var created int64
	if err := row.Scan(&e.Key, &e.Lang, &e.Engine, &e.VTTPath, &e.Confidence, &created); err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	e.CreatedAt = time.Unix(created, 0)
	return &e, true, nil
}

// PutSubtitle upserts a cached subtitle.
func (s *Store) PutSubtitle(ctx context.Context, e SubtitleEntry) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO subtitles(key, lang, engine, vtt_path, confidence, created_at)
		 VALUES(?,?,?,?,?,?)
		 ON CONFLICT(key) DO UPDATE SET engine=excluded.engine, vtt_path=excluded.vtt_path,
		   confidence=excluded.confidence, created_at=excluded.created_at`,
		e.Key, e.Lang, e.Engine, e.VTTPath, e.Confidence, time.Now().Unix())
	return err
}

// --- metadata cache ---

// GetMeta returns a cached JSON payload if present and not expired.
func (s *Store) GetMeta(ctx context.Context, key string) (string, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT payload, created_at, ttl_seconds FROM meta_cache WHERE key = ?`, key)
	var payload string
	var created, ttl int64
	if err := row.Scan(&payload, &created, &ttl); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	// ttl == 0 means "never expires"; ttl < 0 means "already expired"; ttl > 0
	// is a normal relative expiry.
	if ttl < 0 || (ttl > 0 && time.Now().Unix()-created > ttl) {
		return "", false, nil // expired
	}
	return payload, true, nil
}

// PutMeta stores a JSON payload with a TTL.
func (s *Store) PutMeta(ctx context.Context, key, payload string, ttl time.Duration) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta_cache(key, payload, created_at, ttl_seconds)
		 VALUES(?,?,?,?)
		 ON CONFLICT(key) DO UPDATE SET payload=excluded.payload, created_at=excluded.created_at, ttl_seconds=excluded.ttl_seconds`,
		key, payload, time.Now().Unix(), int64(ttl.Seconds()))
	return err
}

func cacheKeyErr(parts ...string) error {
	return fmt.Errorf("store: invalid key parts %v", parts)
}

var _ = cacheKeyErr
