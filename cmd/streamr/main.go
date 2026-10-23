// Command streamr runs the media discovery + torrent-streaming server.
//
// Concurrency model (this process is multi-threaded throughout):
//   - net/http serves every request on its own goroutine, so searches,
//     source lookups, and multiple simultaneous streams run in parallel.
//   - the source Resolver fans out to every indexer concurrently.
//   - the embedded torrent engine downloads pieces from many peers in parallel.
//   - ffmpeg transcodes use all CPU cores (-threads 0).
//   - subtitle jobs run on background goroutines so playback never blocks.
//   - idle-session teardown runs sessions down concurrently.
//
// GOMAXPROCS defaults to the number of CPUs, which we log at startup.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/0xisalive/streamr/internal/config"
	"github.com/0xisalive/streamr/internal/discovery"
	"github.com/0xisalive/streamr/internal/engine"
	"github.com/0xisalive/streamr/internal/server"
	"github.com/0xisalive/streamr/internal/source"
	"github.com/0xisalive/streamr/internal/store"
	"github.com/0xisalive/streamr/internal/subtitle"
	"github.com/0xisalive/streamr/internal/transcode"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	for _, warn := range cfg.Validate() {
		log.Printf("WARN: %s", warn)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	log.Printf("streamr starting: %s", cfg.String())
	log.Printf("concurrency: GOMAXPROCS=%d (CPUs=%d)", runtime.GOMAXPROCS(0), runtime.NumCPU())

	// Discovery (TMDB).
	tmdb := discovery.NewClient(cfg.TMDBAPIKey)

	// SQLite cache (pure-Go driver; keeps the FreeBSD static build cgo-free).
	st, err := store.Open(filepath.Join(cfg.DataDir, "streamr.db"))
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// Resolver: register every configured source → maximize coverage.
	resolver := source.NewResolver(20*time.Second, source.DefaultScorer{})
	for _, ic := range cfg.Torznab {
		resolver.Register(source.NewTorznabSource(ic.Name, ic.URL, ic.APIKey, ic.Enabled))
		if ic.Enabled {
			log.Printf("registered torrent indexer: %s", ic.Name)
		}
	}
	for _, ic := range cfg.Newznab {
		resolver.Register(source.NewNewznabSource(ic.Name, ic.URL, ic.APIKey, ic.Enabled))
		if ic.Enabled {
			log.Printf("registered usenet indexer: %s", ic.Name)
		}
	}

	// Streaming engine. Default is the EMBEDDED engine — no external torrent
	// client to install or configure; it ships inside this binary.
	eng := buildEngine(cfg)

	// Transcode layer (ffmpeg/ffprobe). Direct-play works without it.
	prober := transcode.Prober{}
	trans := transcode.Transcoder{}
	if prober.Available() {
		log.Printf("ffprobe found: container/codec detection enabled")
	} else {
		log.Printf("WARN: ffprobe not found: all media served as direct-play (mkv/HEVC may not play in-browser)")
	}

	// Subtitle coordinator. Whisper-on-the-go GENERATION is the quota-free
	// default; OpenSubtitles (if a key is set) is tried first and aligned.
	subs := buildSubtitles(cfg, st)

	srv := server.New(server.Deps{
		Config:   cfg,
		TMDB:     tmdb,
		Resolver: resolver,
		Engine:   eng,
		Subs:     subs,
		Prober:   prober,
		Trans:    trans,
	})

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		_ = httpSrv.Shutdown(ctx)
		if c, ok := eng.(interface{ Close() error }); ok {
			_ = c.Close()
		}
	}()

	log.Printf("listening on %s (web dir: %q)", cfg.Addr, cfg.WebDir)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
	log.Println("stopped")
}

func buildEngine(cfg config.Config) source.Engine {
	switch cfg.Engine {
	case "qbittorrent":
		if cfg.QbitURL == "" {
			log.Printf("WARN: engine=qbittorrent but no qbit_url; streaming disabled")
			return nil
		}
		qb, err := engine.NewQBittorrent(cfg.QbitURL, cfg.QbitUser, cfg.QbitPass, cfg.QbitSaveDir)
		if err != nil {
			log.Fatalf("init qbittorrent engine: %v", err)
		}
		log.Printf("torrent engine: qBittorrent @ %s", cfg.QbitURL)
		return qb
	default: // "embedded"
		dir := filepath.Join(cfg.DataDir, "torrents")
		a, err := engine.NewAnacrolix(dir)
		if err != nil {
			log.Fatalf("init embedded torrent engine: %v", err)
		}
		log.Printf("torrent engine: embedded (anacrolix) data=%s — no external client needed", dir)
		return a
	}
}

func buildSubtitles(cfg config.Config, st *store.Store) *subtitle.Coordinator {
	// Aligner tiers (used when an external provider supplies a raw subtitle).
	pipeline := subtitle.NewPipeline(
		cfg.SubMinConfidence,
		subtitle.FfsubsyncEngine{},
		subtitle.AlassEngine{},
		subtitle.WhisperXEngine{Enabled: cfg.WhisperXEnabled},
	)

	var providers []subtitle.Provider
	if cfg.OpenSubtitlesAPIKey != "" {
		providers = append(providers, subtitle.NewOpenSubtitlesProvider(cfg.OpenSubtitlesAPIKey, cfg.OpenSubtitlesUA))
		log.Printf("subtitle provider: opensubtitles (rate-limited; whisper-gen fallback active)")
	}

	var gen subtitle.Generator
	if cfg.WhisperGenEnabled {
		g := subtitle.WhisperGenEngine{Model: cfg.WhisperModel, Device: cfg.WhisperDevice}
		gen = g
		if g.Available() {
			log.Printf("subtitle generator: whisper-on-the-go (model=%s device=%s) — default, quota-free",
				cfg.WhisperModel, cfg.WhisperDevice)
		} else {
			log.Printf("WARN: whisper-gen enabled but no whisper CLI on PATH; install faster-whisper or openai-whisper")
		}
	}

	coord := subtitle.NewCoordinator(subtitle.Options{
		Providers: providers,
		Generator: gen,
		Pipeline:  pipeline,
		Cache:     st,
		CacheDir:  filepath.Join(cfg.DataDir, "subs"),
	})
	if !coord.Enabled() {
		log.Printf("WARN: no subtitle provider or generator available: subtitles disabled")
	}
	return coord
}
