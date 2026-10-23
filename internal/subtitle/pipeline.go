// Package subtitle implements the tiered subtitle-alignment pipeline.
//
// OpenSubtitles files are usually timed against a DIFFERENT release (different
// framerate, cut, intro length, ad breaks), producing three distinct kinds of
// misalignment. No single tool fixes all three, so the pipeline chains them
// cheapest-first and — critically — never ships a result that scores worse
// than the original:
//
//	Tier 1  ffsubsync  (VAD against the video's audio)  — constant offset + global framerate drift
//	Tier 2  alass      (split-aware)                    — local/piecewise shifts (scene cuts, ad breaks)
//	Tier 3  whisperx   (forced alignment from audio)    — re-derives timing authoritatively; OPT-IN (GPU/PyTorch)
//	Gate    confidence scoring                          — discard the "fix" if it's worse; keep original
//
// All engines are external CLIs, so the pipeline shells out and stays portable
// across Linux and FreeBSD. ffsubsync + alass are the always-available
// baseline; WhisperX is optional because PyTorch is heavy (esp. on FreeBSD).
package subtitle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Engine is one alignment backend (ffsubsync, alass, whisperx).
type Engine interface {
	Name() string
	Available() bool // is the CLI installed / enabled?
	// Sync aligns subPath against the media's audio (audioPath) and writes the
	// result to outPath. Returns a confidence in [0,1]; 0 means "no better than
	// input" / failed.
	Sync(ctx context.Context, req SyncRequest) (confidence float64, err error)
}

// SyncRequest carries everything an engine needs. AudioPath is a wav/aac the
// caller extracts once (with the ffmpeg we already bundle) and reuses across
// tiers, so audio is never re-decoded per engine.
type SyncRequest struct {
	SubPath   string // input subtitle (srt)
	AudioPath string // extracted audio reference
	VideoPath string // some engines prefer the full video
	OutPath   string // where to write aligned srt
	Lang      string // ISO code, helps whisper
}

// Pipeline runs the tiers in order and applies the confidence gate.
type Pipeline struct {
	tiers         []Engine
	minConfidence float64 // below this, we keep the original subtitle
}

// NewPipeline wires the tiers. Pass engines in cheap→expensive order. Disabled
// engines (Available()==false) are skipped automatically, so an install
// without WhisperX simply runs ffsubsync+alass.
func NewPipeline(minConfidence float64, tiers ...Engine) *Pipeline {
	return &Pipeline{tiers: tiers, minConfidence: minConfidence}
}

// Result reports what happened, for logging and UI ("synced", "kept original").
type Result struct {
	Engine     string
	Confidence float64
	Applied    bool   // true if the aligned file was accepted over the original
	OutPath    string // path to the subtitle to actually serve
}

// Align runs the tiered pipeline. It stops at the first tier that clears the
// confidence gate. If none do, it returns the ORIGINAL subtitle unchanged —
// never a worse one (guards against the known ffsubsync framerate-stretch
// failure mode where an already-correct sub is silently ruined).
func (p *Pipeline) Align(ctx context.Context, req SyncRequest) (Result, error) {
	if req.SubPath == "" {
		return Result{}, errors.New("subtitle: empty SubPath")
	}
	var lastErr error
	for _, e := range p.tiers {
		if !e.Available() {
			continue
		}
		tierOut := req.OutPath + "." + e.Name() + ".tmp.srt"
		r := req
		r.OutPath = tierOut

		conf, err := e.Sync(ctx, r)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", e.Name(), err)
			continue
		}
		if conf >= p.minConfidence {
			if err := os.Rename(tierOut, req.OutPath); err != nil {
				// fall back to copy if rename across devices fails
				if cerr := copyFile(tierOut, req.OutPath); cerr != nil {
					lastErr = cerr
					continue
				}
				_ = os.Remove(tierOut)
			}
			return Result{Engine: e.Name(), Confidence: conf, Applied: true, OutPath: req.OutPath}, nil
		}
		_ = os.Remove(tierOut) // discard low-confidence attempt
	}

	// Gate failed for every tier: keep the original untouched.
	if req.OutPath != req.SubPath {
		if err := copyFile(req.SubPath, req.OutPath); err != nil {
			return Result{}, errors.Join(lastErr, err)
		}
	}
	return Result{Engine: "none", Confidence: 0, Applied: false, OutPath: req.OutPath}, lastErr
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
