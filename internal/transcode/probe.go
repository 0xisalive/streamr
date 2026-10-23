// Package transcode inspects media and decides how to deliver it to a browser:
// direct-play, container remux, or a full transcode. Browsers natively play
// H.264/AAC in MP4 but not mkv/HEVC/AC3/DTS, which is what most torrents
// actually are — so this package is where the "mkv tax" is paid.
package transcode

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// MediaInfo is the subset of ffprobe output we care about for the decision.
type MediaInfo struct {
	Container  string // "matroska,webm", "mov,mp4,m4a,...", etc.
	VideoCodec string // "h264", "hevc", "av1", ...
	AudioCodec string // "aac", "ac3", "eac3", "dts", ...
	DurationS  float64
	Width      int
	Height     int
}

// Prober wraps ffprobe. Path defaults to "ffprobe" on PATH.
type Prober struct {
	FFprobePath string
}

func (p Prober) bin() string {
	if p.FFprobePath != "" {
		return p.FFprobePath
	}
	return "ffprobe"
}

// Available reports whether ffprobe can be found.
func (p Prober) Available() bool {
	_, err := exec.LookPath(p.bin())
	return err == nil
}

// ffprobe JSON shapes.
type ffprobeOut struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
}

// Probe runs ffprobe on a (possibly partial) file and returns MediaInfo. It
// works on partial files because ffprobe reads only the header/moov atom; for a
// streaming torrent the first pieces are prioritized, so the header is usually
// available early.
func (p Prober) Probe(ctx context.Context, path string) (MediaInfo, error) {
	cmd := exec.CommandContext(ctx, p.bin(),
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return MediaInfo{}, fmt.Errorf("ffprobe %s: %w", path, err)
	}
	var raw ffprobeOut
	if err := json.Unmarshal(out, &raw); err != nil {
		return MediaInfo{}, fmt.Errorf("ffprobe decode: %w", err)
	}

	mi := MediaInfo{Container: raw.Format.FormatName}
	mi.DurationS, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			if mi.VideoCodec == "" {
				mi.VideoCodec = strings.ToLower(s.CodecName)
				mi.Width, mi.Height = s.Width, s.Height
			}
		case "audio":
			if mi.AudioCodec == "" {
				mi.AudioCodec = strings.ToLower(s.CodecName)
			}
		}
	}
	return mi, nil
}
