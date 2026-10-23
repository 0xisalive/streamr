package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/0xisalive/streamr/internal/config"
	"github.com/0xisalive/streamr/internal/discovery"
	"github.com/0xisalive/streamr/internal/engine"
	"github.com/0xisalive/streamr/internal/source"
	"github.com/0xisalive/streamr/internal/transcode"
)

// TestLiveE2E_PlayAndStreamOverHTTP drives the FULL user path over real HTTP
// against a real swarm: /api/play opens the embedded engine on the Sintel
// (Creative Commons) magnet, then GET /api/stream/{id} with a Range header
// returns 206 Partial Content streamed from the torrent. This is the complete
// "press play → bytes in the browser" flow.
//
// Gated behind STREAMR_LIVE=1 (needs network + peers).
func TestLiveE2E_PlayAndStreamOverHTTP(t *testing.T) {
	if os.Getenv("STREAMR_LIVE") != "1" {
		t.Skip("set STREAMR_LIVE=1 to run the live end-to-end HTTP test")
	}

	dir := t.TempDir()
	eng, err := engine.NewAnacrolix(dir)
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	defer eng.Close()

	const sintelMagnet = "magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10&dn=Sintel&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337&tr=udp%3A%2F%2Fexplodie.org%3A6969&tr=wss%3A%2F%2Ftracker.openwebtorrent.com"

	// Stub resolver that returns the Sintel magnet as the top candidate, so
	// /api/autoplay can pick it without needing a configured indexer.
	res := source.NewResolver(5*time.Second, source.DefaultScorer{}, &stubSource{
		cand: source.Candidate{
			Source: "test", Protocol: source.ProtocolTorrent,
			Title: "Sintel.2010.1080p.x264", Magnet: sintelMagnet, Seeders: 1000,
		},
	})

	cfg := config.Default()
	cfg.DataDir = dir
	srv := New(Deps{
		Config:   cfg,
		TMDB:     discovery.NewClient(""),
		Resolver: res,
		Engine:   eng,
		Prober:   transcode.Prober{FFprobePath: "/nonexistent"}, // force direct play
		Trans:    transcode.Transcoder{FFmpegPath: "/nonexistent"},
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. /api/autoplay — zero-config "just press play".
	body, _ := json.Marshal(playRequest{TmdbID: 1, Type: "movie"})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/api/autoplay", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("autoplay: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("autoplay status %d: %s", resp.StatusCode, b)
	}
	var play struct {
		SessionID string `json:"session_id"`
		StreamURL string `json:"stream_url"`
		Delivery  string `json:"delivery"`
	}
	json.NewDecoder(resp.Body).Decode(&play)
	t.Logf("autoplay → session=%s stream=%s delivery=%s", play.SessionID, play.StreamURL, play.Delivery)
	if play.StreamURL == "" {
		t.Fatal("no stream_url returned")
	}

	// 2. GET the stream with a Range header → expect 206 + bytes from the swarm.
	sreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+play.StreamURL, nil)
	sreq.Header.Set("Range", "bytes=0-65535")
	sresp, err := http.DefaultClient.Do(sreq)
	if err != nil {
		t.Fatalf("stream GET: %v", err)
	}
	defer sresp.Body.Close()
	if sresp.StatusCode != http.StatusPartialContent {
		t.Fatalf("stream status = %d, want 206", sresp.StatusCode)
	}
	got, _ := io.ReadAll(sresp.Body)
	if len(got) == 0 {
		t.Fatal("streamed 0 bytes over HTTP Range")
	}
	t.Logf("E2E success: streamed %d bytes over HTTP Range (206) from a live torrent", len(got))
}

type stubSource struct{ cand source.Candidate }

func (s *stubSource) Name() string              { return "stub" }
func (s *stubSource) Protocol() source.Protocol { return source.ProtocolTorrent }
func (s *stubSource) Enabled() bool             { return true }
func (s *stubSource) Search(context.Context, source.Query) ([]source.Candidate, error) {
	return []source.Candidate{s.cand}, nil
}
