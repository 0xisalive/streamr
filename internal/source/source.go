// Package source defines the pluggable interfaces for discovering and
// resolving playable media. Everything that can produce a stream — torrent
// indexers, direct trackers, Usenet, IPFS — implements Source, and the
// Resolver aggregates and health-ranks their candidates.
package source

import (
	"context"
	"io"
	"time"
)

// Protocol identifies the transport a Candidate is delivered over.
type Protocol string

const (
	ProtocolTorrent Protocol = "torrent"
	ProtocolUsenet  Protocol = "usenet"
	ProtocolIPFS    Protocol = "ipfs"
)

// MediaType distinguishes a movie from an episode of a series.
type MediaType string

const (
	MediaMovie   MediaType = "movie"
	MediaEpisode MediaType = "episode"
)

// Query describes what the user wants. Season/Episode are only meaningful for
// MediaEpisode. ImdbID / TmdbID let indexers do ID-based lookups (far more
// reliable than title string matching, especially for obscure titles).
type Query struct {
	Type    MediaType
	Title   string
	Year    int
	Season  int
	Episode int
	ImdbID  string
	TmdbID  int
}

// Candidate is a single playable result from a Source. The Resolver ranks
// these; the session manager turns the chosen one into a stream.
type Candidate struct {
	// Identity
	Source   string   // human-readable origin, e.g. "prowlarr:1337x"
	Protocol Protocol // torrent | usenet | ipfs
	Title    string   // release title, e.g. "Some.Show.S01E01.1080p.WEB.x264-GRP"

	// Locator — exactly one is populated depending on Protocol.
	Magnet  string // torrent: magnet URI (preferred — enables DHT)
	TorrURL string // torrent: .torrent file URL (fallback)
	Infoy   string // torrent: infohash (for dedup across indexers)
	NZBURL  string // usenet: .nzb download URL
	CID     string // ipfs: content identifier

	// Health / quality signals used for ranking.
	Seeders   int           // torrent swarm health (0 == effectively dead)
	Leechers  int           // torrent
	Grabs     int           // usenet/torrent download count (popularity proxy)
	SizeBytes int64         // file/release size
	Age       time.Duration // how old the release is (retention relevance for usenet)
	Published time.Time

	// Parsed release attributes (filled by the release parser).
	Resolution string // "2160p" | "1080p" | "720p" | ...
	VideoCodec string // "h264" | "h265" | "av1" | ...
	Container  string // "mp4" | "mkv" | ...
	Group      string // release group

	// Score is assigned by the Resolver; higher is better.
	Score float64
}

// Source is anything that can be searched for Candidates. Implementations:
// torrent indexers (Prowlarr/Jackett Torznab), Usenet (Newznab), IPFS catalogs.
type Source interface {
	// Name is a stable identifier for logs and candidate attribution.
	Name() string
	// Protocol this source produces.
	Protocol() Protocol
	// Enabled reports whether the source is configured and should be queried.
	Enabled() bool
	// Search returns candidates for the query. Implementations SHOULD honor
	// ctx cancellation (the resolver fans out with a timeout).
	Search(ctx context.Context, q Query) ([]Candidate, error)
}

// Engine turns a chosen Candidate into a seekable, range-readable stream while
// the content is still being fetched. Torrent and Usenet engines implement it;
// the IPFS gateway implements it trivially (native range support).
type Engine interface {
	// Protocols this engine can serve.
	Protocols() []Protocol
	// Open begins fetching the candidate and returns a handle that can be read
	// and seeked. The engine is responsible for playback-ordered fetching
	// (sequential download / piece deadlines) so that reads near the current
	// offset resolve quickly.
	Open(ctx context.Context, c Candidate) (*Stream, error)
}

// Stream is a handle to an in-progress download that supports HTTP Range
// semantics. Size is the full length of the chosen video file; Reader lets the
// stream endpoint seek to any offset (re-prioritizing fetch as needed).
type Stream struct {
	ID        string // session id
	Name      string // video file name within the torrent/nzb
	Size      int64  // total size of the video file in bytes
	Reader    ReadSeekraleCloser
	NewReader func() (ReadSeekraleCloser, error) // independent per-request reader
	Progress  func() float64                     // 0..1 fraction downloaded, for the UI
	Close     func() error                       // tear down the session (also called by idle GC)
}

// OpenReader returns an independent reader when the engine supports it (so
// concurrent Range requests don't contend on a shared seek offset), else falls
// back to the single shared Reader.
func (s *Stream) OpenReader() (ReadSeekraleCloser, bool, error) {
	if s.NewReader != nil {
		r, err := s.NewReader()
		return r, true, err
	}
	return s.Reader, false, nil
}

// ReadSeekraleCloser is io.ReadSeeker + io.Closer. When the player seeks to a
// not-yet-downloaded offset, Seek must trigger re-prioritization so the Read
// that follows does not block indefinitely.
type ReadSeekraleCloser interface {
	io.Reader
	io.Seeker
	io.Closer
}
