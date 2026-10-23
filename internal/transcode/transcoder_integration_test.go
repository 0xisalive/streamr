package transcode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests exercise the real ffmpeg/ffprobe pipeline. They are skipped
// automatically when the binaries are not installed, so the unit suite still
// runs everywhere.

// ffmpegBin / ffprobeBin resolve the binaries, honoring STREAMR_FFMPEG /
// STREAMR_FFPROBE overrides so CI (or a sandbox with ffmpeg in a non-PATH
// location) can point the tests at explicit paths.
func ffmpegBin() string {
	if p := os.Getenv("STREAMR_FFMPEG"); p != "" {
		return p
	}
	return "ffmpeg"
}

func ffprobeBin() string {
	if p := os.Getenv("STREAMR_FFPROBE"); p != "" {
		return p
	}
	return "ffprobe"
}

func ffmpegOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(ffmpegBin()); err != nil {
		if _, serr := os.Stat(ffmpegBin()); serr != nil {
			t.Skip("ffmpeg not installed; skipping integration test")
		}
	}
	if _, err := exec.LookPath(ffprobeBin()); err != nil {
		if _, serr := os.Stat(ffprobeBin()); serr != nil {
			t.Skip("ffprobe not installed; skipping integration test")
		}
	}
}

// proberForTest / transcoderForTest honor the env overrides.
func proberForTest() Prober         { return Prober{FFprobePath: ffprobeBin()} }
func transcoderForTest() Transcoder { return Transcoder{FFmpegPath: ffmpegBin()} }

// synthH264MP4 generates a short H.264/AAC MP4 test clip with ffmpeg.
func synthH264MP4(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "sample.mp4")
	cmd := exec.Command(ffmpegBin(), "-y", "-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", out)
	if o, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("synth mp4: %v: %s", err, o)
	}
	return out
}

// synthH264MKV generates H.264/AAC inside a Matroska container (remux case).
func synthH264MKV(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "sample.mkv")
	cmd := exec.Command(ffmpegBin(), "-y", "-f", "lavfi", "-i", "testsrc=duration=2:size=320x240:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", out)
	if o, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("synth mkv: %v: %s", err, o)
	}
	return out
}

func TestProbeAndDecide_RealMedia(t *testing.T) {
	ffmpegOrSkip(t)
	dir := t.TempDir()
	mp4 := synthH264MP4(t, dir)
	mkv := synthH264MKV(t, dir)

	p := proberForTest()
	ctx := context.Background()

	miMP4, err := p.Probe(ctx, mp4)
	if err != nil {
		t.Fatalf("probe mp4: %v", err)
	}
	if miMP4.VideoCodec != "h264" {
		t.Errorf("mp4 video codec = %q, want h264", miMP4.VideoCodec)
	}
	if Decide(miMP4) != DirectPlay {
		t.Errorf("h264/aac mp4 should be DirectPlay, got %v", Decide(miMP4))
	}

	miMKV, err := p.Probe(ctx, mkv)
	if err != nil {
		t.Fatalf("probe mkv: %v", err)
	}
	if got := Decide(miMKV); got != Remux {
		t.Errorf("h264/aac mkv should be Remux, got %v", got)
	}
}

func TestRemuxToWriter_ProducesFragmentedMP4(t *testing.T) {
	ffmpegOrSkip(t)
	dir := t.TempDir()
	mkv := synthH264MKV(t, dir)

	tr := transcoderForTest()
	p := proberForTest()
	mi, err := p.Probe(context.Background(), mkv)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	outPath := filepath.Join(dir, "out.mp4")
	outF, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := tr.RemuxToWriter(ctx, mkv, mi, outF); err != nil {
		outF.Close()
		t.Fatalf("remux: %v", err)
	}
	outF.Close()

	// Verify the output is a valid MP4 that ffprobe can read back as h264.
	remuxed, err := p.Probe(context.Background(), outPath)
	if err != nil {
		t.Fatalf("probe remuxed: %v", err)
	}
	if remuxed.VideoCodec != "h264" {
		t.Errorf("remuxed video codec = %q, want h264 (copied, not re-encoded)", remuxed.VideoCodec)
	}
	if !strings.Contains(remuxed.Container, "mp4") {
		t.Errorf("remuxed container = %q, want mp4 family", remuxed.Container)
	}
}

func TestStartHLS_ProducesPlaylistAndSegments(t *testing.T) {
	ffmpegOrSkip(t)
	dir := t.TempDir()
	mp4 := synthH264MP4(t, dir)

	tr := transcoderForTest()
	p := proberForTest()
	mi, _ := p.Probe(context.Background(), mp4)
	// Force a transcode by pretending audio needs re-encode is not necessary;
	// instead just run HLS which copies when possible — we only assert output.
	outDir := filepath.Join(dir, "hls")
	ctx := context.Background()
	playlist, stop, err := tr.StartHLS(ctx, mp4, mi, HLSOptions{OutDir: outDir, SegmentSecs: 1})
	if err != nil {
		t.Fatalf("start hls: %v", err)
	}
	defer stop()

	// Wait for the playlist + at least one segment to appear.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(playlist); err == nil {
			segs, _ := filepath.Glob(filepath.Join(outDir, "*.ts"))
			if len(segs) > 0 {
				return // success
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("HLS playlist/segments not produced in time (dir=%s)", outDir)
}
