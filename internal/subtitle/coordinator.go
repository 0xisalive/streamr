package subtitle

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
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

// Generator produces a subtitle from scratch via speech recognition. This is
// the quota-free default (see WhisperGenEngine): external providers are
// rate-limited and patchy on obscure titles, so when they fail we generate.
type Generator interface {
	Name() string
	Available() bool
	Generate(ctx context.Context, ref, outPath, lang string) error
}

// FetchRequest identifies the title to fetch/generate subtitles for.
type FetchRequest struct {
	Title     string
	ImdbID    string
	Lang      string
	Season    int
	Episode   int
	ReleaseID string // stable id for caching (infohash or candidate title)
}

// SubtitleCache persists generated/aligned subtitles so expensive Whisper runs
// happen once per (release, lang). Implemented by internal/store.
type SubtitleCache interface {
	GetVTT(ctx context.Context, key string) (vttPath string, ok bool)
	PutVTT(ctx context.Context, key, lang, engine, vttPath string, confidence float64)
}

// Coordinator performs the end-to-end subtitle job for a playback session.
//
// Order of operations (designed so the user does nothing and always gets subs):
//
//  0. cache hit?                         → serve immediately
//  1. external Provider (OpenSubtitles)  → align via tiered Pipeline
//     · provider missing / quota hit / no result → skip to generation
//  2. Whisper-on-the-go Generator        → transcribe audio directly (DEFAULT fallback)
//  3. convert winner to WebVTT, cache it
//
// Both the provider+align path and the generation path extract reference audio
// exactly once and reuse it.
type Coordinator struct {
	providers []Provider
	generator Generator // may be nil if no whisper CLI available
	pipeline  *Pipeline
	cache     SubtitleCache // may be nil
	cacheDir  string
}

// Options configures a Coordinator.
type Options struct {
	Providers []Provider
	Generator Generator
	Pipeline  *Pipeline
	Cache     SubtitleCache
	CacheDir  string
}

func NewCoordinator(opts Options) *Coordinator {
	return &Coordinator{
		providers: opts.Providers,
		generator: opts.Generator,
		pipeline:  opts.Pipeline,
		cache:     opts.Cache,
		cacheDir:  opts.CacheDir,
	}
}

// Enabled reports whether the coordinator can produce subtitles at all.
func (c *Coordinator) Enabled() bool {
	return len(c.providers) > 0 || (c.generator != nil && c.generator.Available())
}

// CoordResult of the coordinated job.
type CoordResult struct {
	VTTPath   string
	Engine    string
	Provider  string
	Applied   bool
	FromCache bool
}

// Prepare runs the full flow. videoPath is the (partial) media file to align or
// transcribe against; it must be readable.
func (c *Coordinator) Prepare(ctx context.Context, sessionID, videoPath string, req FetchRequest) (CoordResult, error) {
	lang := req.Lang
	if lang == "" {
		lang = "en"
	}
	key := cacheKey(req.ReleaseID, req.Title, lang)

	// 0. Cache hit.
	if c.cache != nil {
		if vtt, ok := c.cache.GetVTT(ctx, key); ok {
			if _, err := os.Stat(vtt); err == nil {
				return CoordResult{VTTPath: vtt, Engine: "cache", FromCache: true}, nil
			}
		}
	}

	sessDir := filepath.Join(c.cacheDir, sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		return CoordResult{}, err
	}

	// Extract reference audio ONCE (reused by align + generate). Non-fatal.
	audioPath := filepath.Join(sessDir, "ref.wav")
	if err := ExtractAudio(ctx, videoPath, audioPath, 0); err != nil {
		audioPath = "" // fall back to video input for engines that accept it
	}
	ref := videoPath
	if audioPath != "" {
		ref = audioPath
	}

	finalSRT := filepath.Join(sessDir, "final.srt")
	engine, provider, confidence, applied := "", "", 0.0, false

	// 1. Try external provider + alignment.
	rawSRT, providerName := c.tryProviders(ctx, req, sessDir)
	if rawSRT != "" {
		pres, err := c.pipeline.Align(ctx, SyncRequest{
			SubPath:   rawSRT,
			AudioPath: audioPath,
			VideoPath: videoPath,
			OutPath:   finalSRT,
			Lang:      lang,
		})
		if err == nil {
			engine, provider, confidence, applied = pres.Engine, providerName, pres.Confidence, pres.Applied
		} else {
			// Align failed hard; keep the raw subtitle.
			if cerr := copyFile(rawSRT, finalSRT); cerr == nil {
				engine, provider = "raw", providerName
			}
		}
	}

	// 2. Fallback / default: Whisper-on-the-go generation. Timings are derived
	// from the audio itself so they are inherently in sync; there is no
	// reference to score against, so "confidence" here is not an alignment
	// score — it is a cache annotation marking the source as generated. We do
	// not inflate it to imply verified quality.
	if engine == "" && c.generator != nil && c.generator.Available() {
		if err := c.generator.Generate(ctx, ref, finalSRT, langOrAuto(req.Lang)); err != nil {
			return CoordResult{}, fmt.Errorf("subtitle: generation failed and no provider succeeded: %w", err)
		}
		engine, confidence, applied = c.generator.Name(), 0.0, true
	}

	if engine == "" {
		return CoordResult{}, fmt.Errorf("subtitle: no provider and no generator available")
	}

	// 3. Convert to WebVTT for the browser. The final VTT goes in a STABLE
	// cache location (not the per-session dir, which is cleaned on teardown) so
	// a cached entry's path remains valid for reuse across sessions.
	vttDir := filepath.Join(c.cacheDir, "cache")
	if err := os.MkdirAll(vttDir, 0o755); err != nil {
		return CoordResult{}, err
	}
	vttPath := filepath.Join(vttDir, key+".vtt")
	if err := SRTToVTT(finalSRT, vttPath); err != nil {
		return CoordResult{}, fmt.Errorf("subtitle: srt->vtt: %w", err)
	}

	if c.cache != nil {
		c.cache.PutVTT(ctx, key, lang, engine, vttPath, confidence)
	}

	return CoordResult{VTTPath: vttPath, Engine: engine, Provider: provider, Applied: applied}, nil
}

// tryProviders returns the first provider subtitle that succeeds. Rate-limit or
// not-found errors simply advance to the next provider (and ultimately to
// generation), so a quota-exhausted OpenSubtitles never blocks playback subs.
func (c *Coordinator) tryProviders(ctx context.Context, req FetchRequest, destDir string) (srtPath, name string) {
	for _, p := range c.providers {
		path, err := p.Fetch(ctx, req, destDir)
		if err == nil && path != "" {
			return path, p.Name()
		}
	}
	return "", ""
}

func langOrAuto(lang string) string {
	// Empty → let whisper auto-detect the spoken language.
	return lang
}

func cacheKey(releaseID, title, lang string) string {
	h := sha1.Sum([]byte(releaseID + "|" + title + "|" + lang))
	return "sub:" + hex.EncodeToString(h[:])
}
