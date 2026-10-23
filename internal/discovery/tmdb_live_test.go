package discovery

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestTMDB_LiveKDramaSearch hits the real TMDB API to search for a K-drama and
// resolve its IMDb id (which streamr feeds to indexers for reliable ID-based
// search of obscure titles). Requires a key: STREAMR_TMDB_API_KEY=...
// Gated so offline CI skips it.
func TestTMDB_LiveKDramaSearch(t *testing.T) {
	key := os.Getenv("STREAMR_TMDB_API_KEY")
	if key == "" {
		t.Skip("set STREAMR_TMDB_API_KEY to run the live TMDB test")
	}
	c := NewClient(key)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	results, err := c.Search(ctx, "Squid Game")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results for a very popular K-drama; API likely misconfigured")
	}
	top := results[0]
	t.Logf("top result: %q (%s, %d) tmdb=%d rating=%.1f", top.Title, top.Type, top.Year, top.TmdbID, top.Rating)

	if top.Type == "tv" {
		imdb, err := c.ExternalIDs(ctx, top.TmdbID, "tv")
		if err != nil {
			t.Fatalf("external ids: %v", err)
		}
		t.Logf("resolved IMDb id: %s", imdb)
		if imdb == "" {
			t.Error("expected a non-empty IMDb id for a major title")
		}
	}
}
