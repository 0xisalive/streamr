package subtitle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Provider fetches a raw (likely-misaligned) subtitle for a title. The classic
// implementation is an OpenSubtitles client, but any source works. Returns the
// path to a downloaded .srt.
type Provider interface {
	Name() string
	// Fetch downloads the best subtitle for the request into destDir and
	// returns its path. lang is an ISO code (e.g. "en").
	Fetch(ctx context.Context, req FetchRequest, destDir string) (srtPath string, err error)
}

// FetchRequest identifies the title to fetch subtitles for.
type FetchRequest struct {
	Title   string
	ImdbID  string
	Lang    string
	Season  int
	Episode int
}

// Coordinator performs the end-to-end subtitle job for a playback session:
//
//  1. fetch a raw subtitle (Provider)
//  2. extract reference audio once (ffmpeg)
//  3. run the tiered alignment Pipeline (with the never-ship-worse gate)
//  4. convert the aligned .srt to WebVTT for the browser <track>
//
// It is designed to run asynchronously after /play returns; the server swaps
// the subtitle track in when GetVTT becomes available.
type Coordinator struct {
	providers []Provider
	pipeline  *Pipeline
	cacheDir  string
}

func NewCoordinator(cacheDir string, pipeline *Pipeline, providers ...Provider) *Coordinator {
	return &Coordinator{providers: providers, pipeline: pipeline, cacheDir: cacheDir}
}

// Result of the coordinated job.
type CoordResult struct {
	VTTPath  string
	Pipeline Result
	Provider string
}

// Prepare runs the full fetch→align→convert flow. videoPath is the (partial)
// media file to align against; it must be readable. On success VTTPath points
// to a browser-ready WebVTT file.
func (c *Coordinator) Prepare(ctx context.Context, sessionID, videoPath string, req FetchRequest) (CoordResult, error) {
	if len(c.providers) == 0 {
		return CoordResult{}, fmt.Errorf("subtitle: no providers configured")
	}
	sessDir := filepath.Join(c.cacheDir, sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		return CoordResult{}, err
	}

	// 1. Fetch raw subtitle from the first provider that succeeds.
	var srtPath, providerName string
	var lastErr error
	for _, p := range c.providers {
		path, err := p.Fetch(ctx, req, sessDir)
		if err == nil && path != "" {
			srtPath, providerName = path, p.Name()
			break
		}
		lastErr = err
	}
	if srtPath == "" {
		return CoordResult{}, fmt.Errorf("subtitle: all providers failed: %w", lastErr)
	}

	// 2. Extract reference audio once (first few minutes is plenty for VAD).
	audioPath := filepath.Join(sessDir, "ref.wav")
	if err := ExtractAudio(ctx, videoPath, audioPath, 0); err != nil {
		// Audio extraction failing is non-fatal: fall back to video-based sync.
		audioPath = ""
	}

	// 3. Align through the tiered pipeline (keeps original if no tier beats it).
	alignedSRT := filepath.Join(sessDir, "aligned.srt")
	pres, err := c.pipeline.Align(ctx, SyncRequest{
		SubPath:   srtPath,
		AudioPath: audioPath,
		VideoPath: videoPath,
		OutPath:   alignedSRT,
		Lang:      req.Lang,
	})
	if err != nil {
		// Pipeline returns the original on gate failure; a hard error here means
		// something lower-level broke. Still try to serve the raw subtitle.
		alignedSRT = srtPath
	}

	// 4. Convert to WebVTT for the browser.
	vttPath := filepath.Join(sessDir, "subtitle.vtt")
	if err := SRTToVTT(alignedSRT, vttPath); err != nil {
		return CoordResult{}, fmt.Errorf("subtitle: srt->vtt: %w", err)
	}

	return CoordResult{VTTPath: vttPath, Pipeline: pres, Provider: providerName}, nil
}
