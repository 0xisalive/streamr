package transcode

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// Transcoder drives ffmpeg for the Remux and Transcode delivery modes.
type Transcoder struct {
	FFmpegPath string
}

func (t Transcoder) bin() string {
	if t.FFmpegPath != "" {
		return t.FFmpegPath
	}
	return "ffmpeg"
}

// Available reports whether ffmpeg can be found.
func (t Transcoder) Available() bool {
	_, err := exec.LookPath(t.bin())
	return err == nil
}

// RemuxArgs builds the ffmpeg arguments to repackage browser-native codecs into
// a fragmented MP4 written to stdout. Fragmented MP4 (movflags) is required so
// the output is streamable without a seekable destination — the moov atom is
// written up front. This is cheap: streams are copied, not re-encoded.
func RemuxArgs(input string) []string {
	return []string{
		"-hide_banner", "-loglevel", "error",
		"-threads", "0",
		"-i", input,
		"-c", "copy",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4",
		"pipe:1",
	}
}

// RemuxToWriter streams a fragmented-MP4 remux of input to w. It honors ctx so
// the stream can be torn down when the client disconnects. mi informs whether
// only audio needs a re-encode (some "playable" video pairs ship with AC3/DTS
// audio in an otherwise-copyable mkv).
func (t Transcoder) RemuxToWriter(ctx context.Context, input string, mi MediaInfo, w io.Writer) error {
	args := RemuxArgs(input)
	// If audio is not browser-playable but video is, re-encode only audio.
	if NeedsAudioReencode(mi) {
		args = []string{
			"-hide_banner", "-loglevel", "error",
			"-i", input,
			"-c:v", "copy",
			"-c:a", "aac", "-b:a", "192k",
			"-movflags", "frag_keyframe+empty_moov+default_base_moof",
			"-f", "mp4",
			"pipe:1",
		}
	}
	cmd := exec.CommandContext(ctx, t.bin(), args...)
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// HLSOptions configures an HLS transcode.
type HLSOptions struct {
	OutDir      string // directory to write playlist + segments
	SegmentSecs int    // target segment duration (default 6)
	CRF         int    // x264 quality (lower=better; default 23)
	Preset      string // x264 preset (default "veryfast")
	MaxHeight   int    // optional downscale cap (0 = keep source)
}

func (o *HLSOptions) defaults() {
	if o.SegmentSecs == 0 {
		o.SegmentSecs = 6
	}
	if o.CRF == 0 {
		o.CRF = 23
	}
	if o.Preset == "" {
		o.Preset = "veryfast"
	}
}

// HLSArgs builds the ffmpeg arguments for an HLS transcode. Video is re-encoded
// to H.264 only if needed (NeedsVideoReencode); audio to AAC only if needed.
// The playlist uses a growing (event) list so playback can start before the
// transcode finishes.
func HLSArgs(input string, mi MediaInfo, o HLSOptions) ([]string, string) {
	o.defaults()
	playlist := filepath.Join(o.OutDir, "index.m3u8")
	segPattern := filepath.Join(o.OutDir, "seg%05d.ts")

	args := []string{
		"-hide_banner", "-loglevel", "error",
		// Use all available cores for decode/encode. "0" lets ffmpeg pick the
		// optimal thread count for the codec.
		"-threads", "0",
		"-i", input,
	}
	// Video
	if NeedsVideoReencode(mi) {
		// x264 is internally multi-threaded; -threads 0 (above) enables frame /
		// slice threading across cores, which is the dominant transcode cost.
		args = append(args, "-c:v", "libx264", "-preset", o.Preset, "-crf", strconv.Itoa(o.CRF))
		if o.MaxHeight > 0 {
			args = append(args, "-vf", fmt.Sprintf("scale=-2:min(%d\\,ih)", o.MaxHeight))
		}
	} else {
		args = append(args, "-c:v", "copy")
	}
	// Audio
	if NeedsAudioReencode(mi) || mi.AudioCodec != "aac" {
		args = append(args, "-c:a", "aac", "-b:a", "192k")
	} else {
		args = append(args, "-c:a", "copy")
	}
	// HLS muxer
	args = append(args,
		"-f", "hls",
		"-hls_time", strconv.Itoa(o.SegmentSecs),
		"-hls_playlist_type", "event",
		"-hls_flags", "independent_segments",
		"-hls_segment_filename", segPattern,
		playlist,
	)
	return args, playlist
}

// StartHLS launches an HLS transcode in the background, writing the playlist
// and segments into o.OutDir. It returns the playlist path and a cancel func to
// stop the transcode (called by the session teardown). The caller polls for the
// playlist to appear before serving it.
func (t Transcoder) StartHLS(ctx context.Context, input string, mi MediaInfo, o HLSOptions) (playlist string, stop func(), err error) {
	o.defaults()
	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return "", nil, err
	}
	args, pl := HLSArgs(input, mi, o)

	cctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cctx, t.bin(), args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return "", nil, err
	}
	go func() { _ = cmd.Wait() }() // reap when it finishes/cancels

	return pl, cancel, nil
}
