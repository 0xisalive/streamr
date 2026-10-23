package subtitle

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WhisperGenEngine GENERATES subtitles directly from the media's audio by
// speech recognition — no external subtitle required. This is the default
// subtitle path: external providers like OpenSubtitles are rate-limited (e.g.
// ~20 downloads/day on the free tier) and often lack obscure titles entirely,
// so relying on them alone leaves most content uncaptioned. Whisper-on-the-go
// sidesteps both problems — it always works as long as there is an audio track.
//
// It supports two backends, auto-detected on PATH (preference order):
//   - faster-whisper (CLI `whisper-ctranslate2` or `faster-whisper`) — fast, CPU-friendly
//   - openai whisper (CLI `whisper`) — reference implementation
//
// Unlike the aligner tiers, this produces timings from scratch, so the result
// is inherently in-sync with the audio. Quality depends on model size; the
// Model field defaults to "base" (a good speed/accuracy trade-off on CPU).
type WhisperGenEngine struct {
	Model  string // whisper model: tiny|base|small|medium|large (default "base")
	Device string // "cpu" | "cuda" (default "cpu")
}

func (WhisperGenEngine) Name() string { return "whisper-gen" }

// generator binaries in preference order.
var whisperGenBins = []string{"whisper-ctranslate2", "faster-whisper", "whisper"}

// Available reports whether any supported whisper CLI is installed.
func (WhisperGenEngine) Available() bool {
	for _, b := range whisperGenBins {
		if _, err := exec.LookPath(b); err == nil {
			return true
		}
	}
	return false
}

func (w WhisperGenEngine) model() string {
	if w.Model != "" {
		return w.Model
	}
	return "base"
}

func (w WhisperGenEngine) device() string {
	if w.Device != "" {
		return w.Device
	}
	return "cpu"
}

// Generate transcribes the reference audio/video into an SRT at outPath. lang
// may be empty (auto-detect). It returns the engine-reported path.
//
// This is NOT part of the aligner Engine interface (it needs no input
// subtitle); the Coordinator calls it as a fallback/generator.
func (w WhisperGenEngine) Generate(ctx context.Context, ref, outPath, lang string) error {
	bin := ""
	for _, b := range whisperGenBins {
		if _, err := exec.LookPath(b); err == nil {
			bin = b
			break
		}
	}
	if bin == "" {
		return fmt.Errorf("whisper-gen: no whisper CLI found on PATH")
	}

	outDir := filepath.Dir(outPath)
	if outDir == "" {
		outDir = "."
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	args := []string{ref,
		"--model", w.model(),
		"--output_format", "srt",
		"--output_dir", outDir,
		"--device", w.device(),
	}
	if lang != "" {
		args = append(args, "--language", lang)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("whisper-gen (%s): %w: %s", bin, err, tail(string(out), 400))
	}

	// Locate the produced .srt (named after the input's basename) and move it.
	base := strings.TrimSuffix(filepath.Base(ref), filepath.Ext(ref))
	produced := filepath.Join(outDir, base+".srt")
	if _, err := os.Stat(produced); err != nil {
		// Fallback: find any .srt in the dir that isn't the target.
		entries, rerr := os.ReadDir(outDir)
		if rerr != nil {
			return rerr
		}
		found := ""
		absOut, _ := filepath.Abs(outPath)
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".srt") {
				continue
			}
			cand := filepath.Join(outDir, e.Name())
			if abs, _ := filepath.Abs(cand); abs == absOut {
				continue
			}
			found = cand
			break
		}
		if found == "" {
			return fmt.Errorf("whisper-gen: no .srt produced in %s", outDir)
		}
		produced = found
	}
	if produced != outPath {
		return moveFileGen(produced, outPath)
	}
	return nil
}

func moveFileGen(src, dst string) error {
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
