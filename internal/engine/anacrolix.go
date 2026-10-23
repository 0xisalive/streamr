package engine

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	atorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/0xisalive/streamr/internal/source"
)

// Anacrolix is an embedded BitTorrent engine (github.com/anacrolix/torrent).
// Unlike the qBittorrent engine, it runs in-process — no external daemon — and
// gives us per-reader piece prioritization, which is what makes mid-playback
// seeks responsive: a torrent Reader set to "read-ahead" mode re-prioritizes
// pieces around the current offset on every Seek, so scrubbing to an
// un-downloaded region does not hang indefinitely. This is the recommended
// engine for the real product; it also ships as part of the single static
// binary (works identically on Linux and FreeBSD).
type Anacrolix struct {
	client  *atorrent.Client
	dataDir string
}

// NewAnacrolix starts an embedded torrent client storing data under dataDir.
// A fixed listen port is not required (and would prevent two instances on one
// host, e.g. in tests), so we let the OS assign an ephemeral port.
func NewAnacrolix(dataDir string) (*Anacrolix, error) {
	cfg := atorrent.NewDefaultClientConfig()
	cfg.DataDir = dataDir
	cfg.Seed = false   // we are a leecher/streamer, not a long-term seeder
	cfg.ListenPort = 0 // ephemeral: avoids port clashes between instances
	c, err := atorrent.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Anacrolix{client: c, dataDir: dataDir}, nil
}

func (a *Anacrolix) Protocols() []source.Protocol {
	return []source.Protocol{source.ProtocolTorrent}
}

// Close shuts the client down (releases listeners / storage).
func (a *Anacrolix) Close() error {
	errs := a.client.Close()
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// Open adds the candidate's torrent, waits for metadata, selects the largest
// video file, and returns a Stream whose Reader is an atorrent.Reader in
// read-ahead mode (so HTTP Range seeks reprioritize pieces).
func (a *Anacrolix) Open(ctx context.Context, c source.Candidate) (*source.Stream, error) {
	t, err := a.addTorrent(ctx, c)
	if err != nil {
		return nil, err
	}

	// Wait for metadata (file list) or ctx cancellation.
	select {
	case <-t.GotInfo():
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	vf := largestVideoFile(t)
	if vf == nil {
		t.Drop()
		return nil, fmt.Errorf("anacrolix: no video file in torrent %q", t.Name())
	}

	// Download only the chosen file; set it to high priority and sequential-ish
	// by using a read-ahead Reader.
	vf.SetPriority(atorrent.PiecePriorityHigh)
	reader := vf.NewReader()
	reader.SetReadahead(16 << 20) // 16 MiB read-ahead window
	reader.SetResponsive()        // reprioritize around reads/seeks

	st := &source.Stream{
		ID:     t.InfoHash().HexString(),
		Name:   filepath.Base(vf.DisplayPath()),
		Size:   vf.Length(),
		Reader: &anacrolixReader{r: reader, size: vf.Length()},
		Progress: func() float64 {
			total := vf.Length()
			if total == 0 {
				return 0
			}
			return float64(vf.BytesCompleted()) / float64(total)
		},
		Close: func() error {
			_ = reader.Close()
			t.Drop()
			return nil
		},
	}
	return st, nil
}

func (a *Anacrolix) addTorrent(ctx context.Context, c source.Candidate) (*atorrent.Torrent, error) {
	switch {
	case c.Magnet != "":
		t, err := a.client.AddMagnet(c.Magnet)
		if err != nil {
			return nil, fmt.Errorf("anacrolix add magnet: %w", err)
		}
		return t, nil
	case c.Infoy != "":
		ih := metainfo.NewHashFromHex(strings.TrimSpace(c.Infoy))
		t, _ := a.client.AddTorrentInfoHash(ih)
		return t, nil
	case c.TorrURL != "":
		mi, err := fetchMetaInfo(ctx, c.TorrURL)
		if err != nil {
			return nil, err
		}
		t, err := a.client.AddTorrent(mi)
		if err != nil {
			return nil, fmt.Errorf("anacrolix add torrent: %w", err)
		}
		return t, nil
	default:
		return nil, fmt.Errorf("anacrolix: candidate has no magnet/infohash/torrent url")
	}
}

// largestVideoFile picks the biggest file with a video extension.
func largestVideoFile(t *atorrent.Torrent) *atorrent.File {
	files := t.Files()
	sort.SliceStable(files, func(i, j int) bool { return files[i].Length() > files[j].Length() })
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f.DisplayPath()))
		if videoExts[ext] {
			return f
		}
	}
	return nil
}

// anacrolixReader adapts atorrent.Reader to source.ReadSeekraleCloser and
// exposes Name() == "" (not file-backed on a predictable path); the server's
// subtitle job handles the no-path case gracefully.
type anacrolixReader struct {
	r    atorrent.Reader
	size int64
}

func (ar *anacrolixReader) Read(p []byte) (int, error) { return ar.r.Read(p) }
func (ar *anacrolixReader) Close() error               { return ar.r.Close() }
func (ar *anacrolixReader) Seek(off int64, whence int) (int64, error) {
	return ar.r.Seek(off, whence)
}

// ensure io import is used for interface assertions in tests
var _ io.ReadSeekCloser = (*anacrolixReader)(nil)

// fetchMetaInfo loads a .torrent file from a URL within a short timeout.
func fetchMetaInfo(ctx context.Context, url string) (*metainfo.MetaInfo, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return loadMetaInfoFromURL(cctx, url)
}
