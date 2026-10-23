package engine

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/0xisalive/streamr/internal/source"
)

// TestAnacrolix_LiveStream streams a well-seeded, legally-distributable torrent
// (the Sintel open movie, a Blender Foundation public-domain film) to prove the
// embedded engine: adds a magnet, fetches metadata, selects the video file,
// and serves the opening bytes via the Range-capable reader — i.e. real
// "stream before fully downloaded" behavior.
//
// It is network-dependent and therefore gated behind STREAMR_LIVE=1 so it never
// runs in offline CI.
func TestAnacrolix_LiveStream(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live network test in -short mode")
	}
	if !liveEnabled() {
		t.Skip("set STREAMR_LIVE=1 to run the live torrent-streaming test")
	}

	dir := t.TempDir()
	eng, err := NewAnacrolix(dir)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	defer eng.Close()

	// Sintel — Blender Foundation open movie (Creative Commons). Well-seeded.
	const sintelMagnet = "magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10&dn=Sintel&tr=udp%3A%2F%2Fexplodie.org%3A6969&tr=udp%3A%2F%2Ftracker.coppersurfer.tk%3A6969&tr=udp%3A%2F%2Ftracker.empire-js.us%3A1337&tr=udp%3A%2F%2Ftracker.leechers-paradise.org%3A6969&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337&tr=wss%3A%2F%2Ftracker.btorrent.xyz&tr=wss%3A%2F%2Ftracker.fastcast.nz&tr=wss%3A%2F%2Ftracker.openwebtorrent.com"

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	st, err := eng.Open(ctx, source.Candidate{Magnet: sintelMagnet, Protocol: source.ProtocolTorrent})
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer st.Close()

	t.Logf("streaming file %q (%d bytes)", st.Name, st.Size)
	if st.Size <= 0 {
		t.Fatalf("expected positive file size, got %d", st.Size)
	}

	// Read the first 64 KiB — this must arrive via the swarm (sequential/
	// read-ahead), proving we can play before the whole file is present.
	buf := make([]byte, 64*1024)
	n, err := io.ReadFull(st.Reader, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		t.Fatalf("read opening bytes: %v", err)
	}
	if n == 0 {
		t.Fatal("read 0 bytes from stream")
	}
	t.Logf("successfully streamed first %d bytes; progress=%.4f", n, st.Progress())

	// Seek to the middle and read — exercises piece reprioritization.
	mid := st.Size / 2
	if _, err := st.Reader.Seek(mid, io.SeekStart); err != nil {
		t.Fatalf("seek to middle: %v", err)
	}
	if _, err := io.ReadFull(st.Reader, buf[:4096]); err != nil && err != io.ErrUnexpectedEOF {
		t.Logf("mid-file read returned %v (acceptable if swarm slow within timeout)", err)
	} else {
		t.Logf("mid-file seek+read succeeded")
	}
}
