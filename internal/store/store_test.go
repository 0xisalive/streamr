package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSubtitleCacheRoundTrip(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	key := "sub:abc123"

	if _, ok := s.GetVTT(ctx, key); ok {
		t.Fatal("expected cache miss on empty store")
	}

	s.PutVTT(ctx, key, "en", "whisper-gen", "/data/subs/x/subtitle.vtt", 0.85)

	path, ok := s.GetVTT(ctx, key)
	if !ok {
		t.Fatal("expected cache hit after put")
	}
	if path != "/data/subs/x/subtitle.vtt" {
		t.Errorf("got path %q, want cached path", path)
	}

	// Upsert should overwrite.
	s.PutVTT(ctx, key, "en", "ffsubsync", "/data/subs/x/aligned.vtt", 0.92)
	entry, ok, err := s.GetSubtitle(ctx, key)
	if err != nil || !ok {
		t.Fatalf("get after upsert: ok=%v err=%v", ok, err)
	}
	if entry.Engine != "ffsubsync" || entry.Confidence != 0.92 {
		t.Errorf("upsert didn't take: engine=%s conf=%.2f", entry.Engine, entry.Confidence)
	}
}

func TestMetaCacheTTL(t *testing.T) {
	db := filepath.Join(t.TempDir(), "meta.db")
	s, err := Open(db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if err := s.PutMeta(ctx, "k", `{"a":1}`, time.Hour); err != nil {
		t.Fatal(err)
	}
	payload, ok, err := s.GetMeta(ctx, "k")
	if err != nil || !ok {
		t.Fatalf("meta get: ok=%v err=%v", ok, err)
	}
	if payload != `{"a":1}` {
		t.Errorf("payload = %q", payload)
	}

	// Expired entry (ttl in the past) must miss.
	if err := s.PutMeta(ctx, "expired", "x", -time.Second); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetMeta(ctx, "expired"); ok {
		t.Error("expected expired meta entry to miss")
	}
}
