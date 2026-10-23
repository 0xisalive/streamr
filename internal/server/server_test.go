package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xisalive/streamr/internal/config"
	"github.com/0xisalive/streamr/internal/discovery"
	"github.com/0xisalive/streamr/internal/source"
	"github.com/0xisalive/streamr/internal/transcode"
)

// memReader is an in-memory seekable stream for tests (stands in for a torrent
// file served over Range).
type memReader struct {
	*bytes.Reader
}

func (memReader) Close() error { return nil }

// fakeEngine returns a Stream backed by fixed bytes.
type fakeEngine struct {
	data []byte
	name string
}

func (f *fakeEngine) Protocols() []source.Protocol { return []source.Protocol{source.ProtocolTorrent} }
func (f *fakeEngine) Open(_ context.Context, c source.Candidate) (*source.Stream, error) {
	return &source.Stream{
		ID:     "sess",
		Name:   f.name,
		Size:   int64(len(f.data)),
		Reader: memReader{bytes.NewReader(f.data)},
		// Model a real engine: hand out an INDEPENDENT reader per request so
		// concurrent Range requests don't share a seek offset.
		NewReader: func() (source.ReadSeekraleCloser, error) {
			return memReader{bytes.NewReader(f.data)}, nil
		},
		Progress: func() float64 { return 0.5 },
		Close:    func() error { return nil },
	}, nil
}

func newTestServer(eng source.Engine) *Server {
	cfg := config.Default()
	cfg.DataDir = "/tmp/streamr-test"
	return New(Deps{
		Config:   cfg,
		TMDB:     discovery.NewClient(""), // disabled
		Resolver: source.NewResolver(0, source.DefaultScorer{}),
		Engine:   eng,
		Prober:   transcode.Prober{FFprobePath: "/nonexistent-ffprobe"}, // force direct play
		Trans:    transcode.Transcoder{FFmpegPath: "/nonexistent-ffmpeg"},
	})
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", rec.Code)
	}
}

func TestSearchDisabledWithoutTMDB(t *testing.T) {
	srv := newTestServer(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=squid+game", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 without TMDB key, got %d", rec.Code)
	}
}

// TestStreamRangeRequest is the core streaming guarantee: the stream endpoint
// must honor HTTP Range and return 206 Partial Content with the right slice —
// this is what lets the browser <video> element seek and play progressively.
func TestStreamRangeRequest(t *testing.T) {
	payload := []byte(strings.Repeat("STREAMR-", 2048)) // 16 KiB of known bytes
	srv := newTestServer(&fakeEngine{data: payload, name: "movie.mp4"})

	// Create a session directly (bypass /play's resolver machinery).
	st, _ := (&fakeEngine{data: payload, name: "movie.mp4"}).Open(context.Background(), source.Candidate{})
	sess := &session{id: "s1", stream: st, mode: transcode.DirectPlay}
	srv.sessions.add(sess)

	// Full request → 200 with the whole body.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stream/s1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("full stream status = %d, want 200", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Fatalf("full body mismatch: got %d bytes, want %d", rec.Body.Len(), len(payload))
	}

	// Range request → 206 with exactly bytes [100,199].
	req := httptest.NewRequest(http.MethodGet, "/api/stream/s1", nil)
	req.Header.Set("Range", "bytes=100-199")
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req)
	if rec2.Code != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", rec2.Code)
	}
	body, _ := io.ReadAll(rec2.Result().Body)
	if len(body) != 100 {
		t.Fatalf("range body length = %d, want 100", len(body))
	}
	if !bytes.Equal(body, payload[100:200]) {
		t.Errorf("range body content mismatch")
	}
	if cr := rec2.Header().Get("Content-Range"); !strings.HasPrefix(cr, "bytes 100-199/") {
		t.Errorf("Content-Range = %q, want bytes 100-199/...", cr)
	}
}

func TestProgressEndpoint(t *testing.T) {
	srv := newTestServer(nil)
	st, _ := (&fakeEngine{data: []byte("abc"), name: "x.mp4"}).Open(context.Background(), source.Candidate{})
	srv.sessions.add(&session{id: "p1", stream: st})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/progress/p1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("progress status = %d", rec.Code)
	}
	var out map[string]float64
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["progress"] != 0.5 {
		t.Errorf("progress = %v, want 0.5", out["progress"])
	}
}

func TestStreamSessionNotFound(t *testing.T) {
	srv := newTestServer(nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stream/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for missing session, got %d", rec.Code)
	}
}

func TestConcurrentStreamRequests(t *testing.T) {
	payload := []byte(strings.Repeat("X", 4096))
	srv := newTestServer(nil)
	st, _ := (&fakeEngine{data: payload, name: "c.mp4"}).Open(context.Background(), source.Candidate{})
	srv.sessions.add(&session{id: "c1", stream: st, mode: transcode.DirectPlay})

	// Hammer the same session from many goroutines; must not race or panic.
	const n = 32
	errc := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			req := httptest.NewRequest(http.MethodGet, "/api/stream/c1", nil)
			req.Header.Set("Range", "bytes=0-99")
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusPartialContent {
				errc <- &httpErr{rec.Code}
				return
			}
			errc <- nil
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errc; err != nil {
			t.Fatalf("concurrent request %d failed: %v", i, err)
		}
	}
}

type httpErr struct{ code int }

func (e *httpErr) Error() string { return http.StatusText(e.code) }

// blockingReader lets a test hold a read open while teardown is attempted, to
// verify the session drains in-flight reads before closing.
type blockingReader struct {
	*bytes.Reader
	release  chan struct{}
	closed   *bool
	closedMu *sync.Mutex
}

func (b *blockingReader) Read(p []byte) (int, error) {
	<-b.release // block until the test allows the read to proceed
	return b.Reader.Read(p)
}
func (b *blockingReader) Close() error {
	b.closedMu.Lock()
	*b.closed = true
	b.closedMu.Unlock()
	return nil
}

// TestTeardownWaitsForInFlightRead verifies the GC-race fix: remove() must not
// close the stream while a read is in flight.
func TestTeardownWaitsForInFlightRead(t *testing.T) {
	var closed bool
	var cmu sync.Mutex
	release := make(chan struct{})
	br := &blockingReader{Reader: bytes.NewReader([]byte("hello world")), release: release, closed: &closed, closedMu: &cmu}

	srv := newTestServer(nil)
	closeCalled := make(chan struct{})
	st := &source.Stream{
		Name: "x.mp4", Size: 11, Reader: br,
		Close: func() error { br.Close(); close(closeCalled); return nil },
	}
	sess := &session{id: "tw", stream: st, mode: transcode.DirectPlay}
	srv.sessions.add(sess)

	// Start a stream read that will block inside Read.
	readDone := make(chan struct{})
	go func() {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stream/tw", nil))
		close(readDone)
	}()
	time.Sleep(100 * time.Millisecond) // let the handler enter beginServe + Read

	// Attempt teardown concurrently; it must block until the read drains.
	removed := make(chan struct{})
	go func() { srv.sessions.remove("tw"); close(removed) }()

	select {
	case <-closeCalled:
		t.Fatal("stream.Close() ran while a read was in flight (use-after-free window)")
	case <-time.After(150 * time.Millisecond):
		// Good: teardown is waiting.
	}

	// Release the read; teardown should now complete and Close should run.
	close(release)
	<-readDone
	select {
	case <-removed:
	case <-time.After(2 * time.Second):
		t.Fatal("teardown did not complete after read drained")
	}
	cmu.Lock()
	defer cmu.Unlock()
	if !closed {
		t.Fatal("stream was never closed after teardown")
	}
}
