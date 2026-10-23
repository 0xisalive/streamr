package subtitle

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ExtractAudio pulls a lightweight mono 16kHz WAV from a (possibly partial)
// media file using the ffmpeg we already bundle for transcoding. Every
// alignment tier reads this one extraction instead of re-decoding the video, so
// audio decode happens once per session. A short duration cap keeps it cheap —
// the opening minutes are enough for VAD-based offset detection.
func ExtractAudio(ctx context.Context, videoPath, outWav string, maxSeconds int) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg not found: %w", err)
	}
	args := []string{"-y", "-i", videoPath, "-vn", "-ac", "1", "-ar", "16000"}
	if maxSeconds > 0 {
		args = append(args, "-t", fmt.Sprint(maxSeconds))
	}
	args = append(args, outWav)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg extract audio: %w: %s", err, tail(string(out), 300))
	}
	return nil
}

// SRTToVTT converts a SubRip (.srt) file to WebVTT (.vtt), the only text
// subtitle format browsers consume via <track>. The conversion is purely
// textual: prepend the WEBVTT header and replace comma decimal separators in
// timestamps with periods.
func SRTToVTT(srtPath, vttPath string) error {
	in, err := os.Open(srtPath)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(vttPath)
	if err != nil {
		return err
	}
	defer out.Close()

	w := bufio.NewWriter(out)
	defer w.Flush()
	if _, err := w.WriteString("WEBVTT\n\n"); err != nil {
		return err
	}

	sc := bufio.NewScanner(in)
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "-->") {
			line = strings.ReplaceAll(line, ",", ".")
		}
		if _, err := w.WriteString(line + "\n"); err != nil {
			return err
		}
	}
	return sc.Err()
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
