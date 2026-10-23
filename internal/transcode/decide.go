package transcode

import "strings"

// Mode is the chosen delivery strategy for a piece of media.
type Mode int

const (
	// DirectPlay: codecs AND container are browser-native — serve the raw bytes
	// over HTTP Range. Zero CPU, fastest start.
	DirectPlay Mode = iota
	// Remux: codecs are browser-native but the container is not (e.g. H.264 in
	// mkv). Repackage to fragmented MP4 with `-c copy` — cheap, no re-encode.
	Remux
	// Transcode: a codec is not browser-playable (HEVC/AC3/DTS). Re-encode to
	// H.264/AAC and deliver as HLS. Expensive (CPU/GPU bound).
	Transcode
)

func (m Mode) String() string {
	switch m {
	case DirectPlay:
		return "direct"
	case Remux:
		return "remux"
	case Transcode:
		return "transcode"
	default:
		return "unknown"
	}
}

// browser-playable video codecs (modern evergreen browsers).
var playableVideo = map[string]bool{
	"h264": true, "avc": true, "av1": true, "vp8": true, "vp9": true,
}

// browser-playable audio codecs.
var playableAudio = map[string]bool{
	"aac": true, "mp3": true, "opus": true, "vorbis": true, "flac": true,
}

// browserContainer reports whether ffprobe's format_name denotes a container
// browsers play natively. ffprobe collapses several demuxers into one
// comma-separated name, which needs care:
//   - ".mkv" reports as "matroska,webm" — NOT browser-native (must remux),
//     even though it contains the substring "webm".
//   - a true ".webm" also reports "matroska,webm", but real .webm is rare in
//     torrents; remuxing one is cheap and harmless, so we err toward remux
//     whenever "matroska" is present.
func browserContainer(formatName string) bool {
	f := strings.ToLower(formatName)
	// Matroska (mkv) must be remuxed regardless of the webm alias.
	if strings.Contains(f, "matroska") {
		return false
	}
	return strings.Contains(f, "mp4") ||
		strings.Contains(f, "m4a") ||
		strings.Contains(f, "mov") ||
		strings.Contains(f, "webm")
}

// Decide chooses a delivery Mode from probed MediaInfo. The logic:
//   - any non-playable codec   → Transcode (must re-encode)
//   - playable codecs, bad container → Remux (repackage only)
//   - playable codecs + container → DirectPlay
func Decide(mi MediaInfo) Mode {
	video := strings.ToLower(mi.VideoCodec)
	audio := strings.ToLower(mi.AudioCodec)

	videoOK := playableVideo[video]
	// Empty audio (rare) is treated as OK — nothing to transcode.
	audioOK := audio == "" || playableAudio[audio]

	if !videoOK || !audioOK {
		return Transcode
	}
	if !browserContainer(mi.Container) {
		return Remux
	}
	return DirectPlay
}

// NeedsVideoReencode reports whether the video stream itself must be re-encoded
// (as opposed to just audio or container). Used to pick -c:v copy vs libx264.
func NeedsVideoReencode(mi MediaInfo) bool {
	return !playableVideo[strings.ToLower(mi.VideoCodec)]
}

// NeedsAudioReencode reports whether only the audio needs transcoding.
func NeedsAudioReencode(mi MediaInfo) bool {
	a := strings.ToLower(mi.AudioCodec)
	return a != "" && !playableAudio[a]
}
