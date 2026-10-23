package source

import "strings"

// Scorer assigns a rank to a Candidate for a given Query. A candidate that
// cannot realistically be played (dead torrent swarm with no alternative)
// should score very low or negative.
type Scorer interface {
	Score(q Query, c Candidate) float64
}

// DefaultScorer encodes the ranking policy. The priorities, in order:
//
//  1. Playability — a torrent with no seeders is nearly worthless; usenet with
//     good retention is reliable regardless of popularity (the obscure-content
//     win). This dominates the score.
//  2. Streaming-friendliness — browser-native codecs/containers (H.264 in mp4)
//     avoid a transcode, so they rank above HEVC/mkv.
//  3. Quality tier — prefer 1080p as the sweet spot; 2160p is heavier to
//     stream, 720p lower quality.
//  4. Popularity — grabs/leechers as a tie-breaker (proxy for "known good").
type DefaultScorer struct{}

func (DefaultScorer) Score(q Query, c Candidate) float64 {
	var s float64

	// 1. Playability.
	switch c.Protocol {
	case ProtocolTorrent:
		switch {
		case c.Seeders <= 0:
			s -= 100 // effectively dead; only surfaces if nothing else exists
		case c.Seeders < 3:
			s += 5
		case c.Seeders < 20:
			s += 40
		default:
			s += 80 // healthy swarm → fast start, reliable streaming
		}
	case ProtocolUsenet:
		// Retention-backed: reliable even for obscure titles. Flat high base.
		s += 75
	case ProtocolIPFS:
		s += 30 // works when it works; latency is variable
	}

	// 2. Streaming-friendliness — avoid transcode where possible.
	switch strings.ToLower(c.VideoCodec) {
	case "h264", "avc", "x264":
		s += 25
	case "h265", "hevc", "x265":
		s += 5 // needs transcode in most browsers
	case "av1":
		s += 8 // supported in modern browsers, but decode-heavy
	}
	switch strings.ToLower(c.Container) {
	case "mp4", "m4v":
		s += 15 // direct-play candidate
	case "mkv":
		s += 3 // remux needed at minimum
	}

	// 3. Quality tier.
	switch c.Resolution {
	case "1080p":
		s += 20
	case "2160p", "4k":
		s += 12
	case "720p":
		s += 10
	case "480p", "sd":
		s += 2
	}

	// 4. Popularity tie-breakers (log-ish, capped).
	s += capf(float64(c.Leechers)*0.05, 10)
	s += capf(float64(c.Grabs)*0.02, 10)

	return s
}

func capf(v, max float64) float64 {
	if v > max {
		return max
	}
	return v
}
