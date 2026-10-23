package subtitle

// whisperx is Tier 3 — the authoritative, forced-alignment path. Rather than
// nudging the existing subtitle's timings, it transcribes the media's audio and
// derives word-level timing directly, so the result is as correct as the audio
// allows. It is opt-in (GPU/PyTorch is heavy, especially on FreeBSD), hence the
// Enabled gate. whisperx writes its own .srt into an output directory; we then
// relocate that file to the pipeline's requested OutPath.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WhisperXEngine wraps the `whisperx` CLI. Enabled makes the whole tier opt-in.
type WhisperXEngine struct {
	Enabled bool
}

// Name identifies the engine/tier.
func (WhisperXEngine) Name() string { return "whisperx" }

// Available reports whether the tier is both enabled and the whisperx CLI is
// installed on PATH.
func (w WhisperXEngine) Available() bool {
	if !w.Enabled {
		return false
	}
	_, err := exec.LookPath("whisperx")
	return err == nil
}

// Sync runs whisperx forced alignment over the reference media (video
// preferred, else audio), letting it write an .srt into the output directory,
// then moves that produced file to req.OutPath. On success it returns an
// authoritative confidence of 0.9; a non-zero exit returns (0, err).
func (WhisperXEngine) Sync(ctx context.Context, req SyncRequest) (float64, error) {
	ref := req.VideoPath
	if ref == "" {
		ref = req.AudioPath
	}

	outDir := filepath.Dir(req.OutPath)
	if outDir == "" {
		outDir = "."
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return 0, err
	}

	args := []string{ref, "--output_format", "srt", "--output_dir", outDir}
	if req.Lang != "" {
		args = append(args, "--language", req.Lang)
	}

	cmd := exec.CommandContext(ctx, "whisperx", args...)
	if err := cmd.Run(); err != nil {
		return 0, err
	}

	// whisperx names its output after the input media's base name, e.g.
	// "<name>.srt". Locate it (falling back to any .srt it produced) and move
	// it to the path the pipeline expects.
	produced, err := locateWhisperXOutput(outDir, ref, req.OutPath)
	if err != nil {
		return 0, err
	}
	if produced != req.OutPath {
		if err := moveFile(produced, req.OutPath); err != nil {
			return 0, err
		}
	}

	return 0.9, nil
}

// locateWhisperXOutput finds the .srt whisperx wrote in outDir. It first tries
// the expected "<media-basename>.srt", then falls back to the most plausible
// .srt in the directory (ignoring the pipeline's own target file).
func locateWhisperXOutput(outDir, ref, outPath string) (string, error) {
	base := strings.TrimSuffix(filepath.Base(ref), filepath.Ext(ref))
	expected := filepath.Join(outDir, base+".srt")
	if _, err := os.Stat(expected); err == nil {
		return expected, nil
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		return "", err
	}
	absOut, _ := filepath.Abs(outPath)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.EqualFold(filepath.Ext(e.Name()), ".srt") {
			continue
		}
		cand := filepath.Join(outDir, e.Name())
		if absCand, _ := filepath.Abs(cand); absCand == absOut {
			continue // skip the pipeline's own target file
		}
		return cand, nil
	}
	return "", fmt.Errorf("whisperx: no .srt output found in %s", outDir)
}

// moveFile renames src to dst, falling back to a copy+remove when the two live
// on different filesystems (os.Rename fails cross-device).
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return err
	}
	return os.Remove(src)
}
