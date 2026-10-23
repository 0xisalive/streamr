// Package server wires discovery, source resolution, the streaming engine, and
// the subtitle pipeline behind an HTTP/JSON API and serves the built frontend.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/0xisalive/streamr/internal/config"
	"github.com/0xisalive/streamr/internal/discovery"
	"github.com/0xisalive/streamr/internal/source"
	"github.com/0xisalive/streamr/internal/subtitle"
	"github.com/0xisalive/streamr/internal/transcode"
)

// Server holds all wired dependencies.
type Server struct {
	cfg      config.Config
	tmdb     *discovery.Client
	resolver *source.Resolver
	engine   source.Engine
	subs     *subtitle.Coordinator // may be nil if no subtitle providers configured
	prober   transcode.Prober
	trans    transcode.Transcoder
	sessions *sessionManager
	webDir   string
	dataDir  string

	// candidate cache per (tmdb,type,season,episode) so /play can reference a
	// candidate index returned by a prior /sources call.
	candMu    sync.Mutex
	candCache map[string][]source.Candidate
}

// Deps bundles the server's wired dependencies.
type Deps struct {
	Config   config.Config
	TMDB     *discovery.Client
	Resolver *source.Resolver
	Engine   source.Engine
	Subs     *subtitle.Coordinator
	Prober   transcode.Prober
	Trans    transcode.Transcoder
}

// New builds the server. Engine and Subs may be nil; the API still serves
// discovery/search without them.
func New(d Deps) *Server {
	idle := time.Duration(d.Config.SessionIdleTimeoutMinutes) * time.Minute
	return &Server{
		cfg:       d.Config,
		tmdb:      d.TMDB,
		resolver:  d.Resolver,
		engine:    d.Engine,
		subs:      d.Subs,
		prober:    d.Prober,
		trans:     d.Trans,
		sessions:  newSessionManager(idle),
		webDir:    d.Config.WebDir,
		dataDir:   d.Config.DataDir,
		candCache: make(map[string][]source.Candidate),
	}
}

// Handler returns the configured http.Handler (API + static frontend).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/sources", s.handleSources)
	mux.HandleFunc("POST /api/play", s.handlePlay)
	// Zero-config "just press play": resolve sources and auto-pick the best one.
	mux.HandleFunc("POST /api/autoplay", s.handleAutoPlay)
	mux.HandleFunc("GET /api/stream/{id}", s.handleStream)
	// HLS playlist + segments for the transcode delivery path.
	mux.HandleFunc("GET /api/hls/{id}/{file}", s.handleHLS)
	mux.HandleFunc("GET /api/progress/{id}", s.handleProgress)
	mux.HandleFunc("GET /api/subtitle/{id}", s.handleSubtitle)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Serve the built SPA (if configured) with history-API fallback.
	if s.webDir != "" {
		mux.Handle("/", s.spaHandler())
	}
	return withCORS(mux)
}

func (s *Server) Shutdown(ctx context.Context) {
	s.sessions.shutdown(ctx)
}

// --- handlers ---

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeErr(w, http.StatusBadRequest, "missing q")
		return
	}
	if !s.tmdb.Enabled() {
		writeErr(w, http.StatusServiceUnavailable, "discovery disabled: TMDB api key not configured")
		return
	}
	results, err := s.tmdb.Search(r.Context(), q)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	tmdbID, _ := strconv.Atoi(r.URL.Query().Get("tmdb_id"))
	mType := r.URL.Query().Get("type")
	season, _ := strconv.Atoi(r.URL.Query().Get("season"))
	episode, _ := strconv.Atoi(r.URL.Query().Get("episode"))

	q := source.Query{
		Type:    mediaType(mType),
		TmdbID:  tmdbID,
		Season:  season,
		Episode: episode,
	}
	// Enrich with IMDb id + title for reliable ID-based indexer search.
	if s.tmdb.Enabled() && tmdbID != 0 {
		if imdb, err := s.tmdb.ExternalIDs(r.Context(), tmdbID, tmdbAPIType(mType)); err == nil {
			q.ImdbID = imdb
		}
	}

	candidates, errs := s.resolver.Resolve(r.Context(), q)
	for _, e := range errs {
		log.Printf("source %s error: %v", e.Source, e.Err)
	}
	s.cacheCandidates(cacheKey(tmdbID, mType, season, episode), candidates)
	writeJSON(w, http.StatusOK, map[string]any{"candidates": candidates})
}

type playRequest struct {
	CandidateIndex int    `json:"candidate_index"`
	TmdbID         int    `json:"tmdb_id"`
	Type           string `json:"type"`
	Season         int    `json:"season"`
	Episode        int    `json:"episode"`
}

func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeErr(w, http.StatusServiceUnavailable, "streaming engine not configured")
		return
	}
	var req playRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	cands, ok := s.getCachedCandidates(cacheKey(req.TmdbID, req.Type, req.Season, req.Episode))
	if !ok || req.CandidateIndex < 0 || req.CandidateIndex >= len(cands) {
		writeErr(w, http.StatusBadRequest, "candidate not found; call /api/sources first")
		return
	}
	s.startPlayback(w, r, cands[req.CandidateIndex], req)
}

// handleAutoPlay is the zero-config path: given only a title (tmdb id + type),
// it resolves sources, auto-picks the highest-ranked playable candidate, and
// starts streaming — so the user truly only searches and presses play.
func (s *Server) handleAutoPlay(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeErr(w, http.StatusServiceUnavailable, "streaming engine not configured")
		return
	}
	var req playRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}

	q := source.Query{
		Type:    mediaType(req.Type),
		TmdbID:  req.TmdbID,
		Season:  req.Season,
		Episode: req.Episode,
	}
	if s.tmdb.Enabled() && req.TmdbID != 0 {
		if imdb, err := s.tmdb.ExternalIDs(r.Context(), req.TmdbID, tmdbAPIType(req.Type)); err == nil {
			q.ImdbID = imdb
		}
	}
	cands, errs := s.resolver.Resolve(r.Context(), q)
	for _, e := range errs {
		log.Printf("autoplay source %s error: %v", e.Source, e.Err)
	}
	if len(cands) == 0 {
		writeErr(w, http.StatusNotFound, "no playable sources found for this title")
		return
	}
	// Resolver already ranks best-first; take the top candidate.
	s.startPlayback(w, r, cands[0], req)
}

// startPlayback opens the engine stream for a candidate, creates a session,
// probes the media to choose a delivery mode (direct/remux/HLS), kicks off the
// async subtitle job, and returns the stream/subtitle URLs.
func (s *Server) startPlayback(w http.ResponseWriter, _ *http.Request, chosen source.Candidate, req playRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	stream, err := s.engine.Open(ctx, chosen)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "failed to open stream: "+err.Error())
		return
	}

	id := newID()
	sess := &session{id: id, stream: stream, candidate: chosen}
	s.sessions.add(sess)

	// Decide delivery mode by probing the (partial) file. Falls back to direct
	// play if probing is unavailable (e.g. no ffprobe, or no file path).
	streamURL := "/api/stream/" + id
	mode := transcode.DirectPlay
	if path := videoPathOf(stream); path != "" && s.prober.Available() {
		if mi, perr := s.prober.Probe(ctx, path); perr == nil {
			mode = transcode.Decide(mi)
			sess.mediaInfo = mi
			sess.videoPath = path
			if mode == transcode.Transcode {
				streamURL = s.startHLS(id, sess, path, mi)
			}
		}
	}
	sess.mode = mode

	if s.subs != nil {
		s.startSubtitleJob(id, chosen, req)
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"session_id":   id,
		"stream_url":   streamURL,
		"subtitle_url": "/api/subtitle/" + id,
		"delivery":     mode.String(),
	})
}

// startHLS launches a background HLS transcode for the session and returns the
// playlist URL the client should load with hls.js.
func (s *Server) startHLS(id string, sess *session, path string, mi transcode.MediaInfo) string {
	outDir := filepath.Join(s.dataDir, "hls", id)
	ctx, cancel := context.WithCancel(context.Background())
	playlist, stop, err := s.trans.StartHLS(ctx, path, mi, transcode.HLSOptions{OutDir: outDir})
	if err != nil {
		cancel()
		log.Printf("hls start failed for %s: %v; falling back to direct", id, err)
		sess.mode = transcode.DirectPlay // keep session state consistent
		return "/api/stream/" + id
	}
	sess.hlsDir = outDir
	sess.hlsStop = func() { stop(); cancel() }

	// Wait (bounded) for ffmpeg to actually write the playlist + first segment
	// before handing the URL to the client, so the initial manifest fetch does
	// not 404. hls.js would retry, but this removes the race the code claimed
	// to handle.
	if !waitForHLSReady(playlist, outDir, 20*time.Second) {
		log.Printf("hls %s: playlist not ready within timeout; client will retry", id)
	}
	return "/api/hls/" + id + "/index.m3u8"
}

// startSubtitleJob fetches/generates + aligns a subtitle for the session in the
// background so playback begins immediately. The subtitle Coordinator will fall
// back to Whisper-on-the-go generation when an external provider is unavailable
// or rate-limited, so a track is produced even for obscure titles.
func (s *Server) startSubtitleJob(sessionID string, chosen source.Candidate, req playRequest) {
	sess, ok := s.sessions.get(sessionID)
	if !ok {
		return
	}
	videoPath := videoPathOf(sess.stream)
	if videoPath == "" {
		return // engine with no on-disk path; nothing to extract audio from yet
	}

	imdb := ""
	if s.tmdb.Enabled() && req.TmdbID != 0 {
		if id, err := s.tmdb.ExternalIDs(context.Background(), req.TmdbID, tmdbAPIType(req.Type)); err == nil {
			imdb = id
		}
	}
	releaseID := chosen.Infoy
	if releaseID == "" {
		releaseID = chosen.Title
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		res, err := s.subs.Prepare(ctx, sessionID, videoPath, subtitle.FetchRequest{
			Title:     chosen.Title,
			ImdbID:    imdb,
			Lang:      "en",
			Season:    req.Season,
			Episode:   req.Episode,
			ReleaseID: releaseID,
		})
		if err != nil {
			log.Printf("subtitle job %s failed: %v", sessionID, err)
			return
		}
		if sess, ok := s.sessions.get(sessionID); ok {
			sess.mu.Lock()
			sess.subtitle = res.VTTPath
			// Record the per-session subtitle working dir for cleanup on
			// teardown (unless it is the shared cache path for a cache hit).
			if !res.FromCache {
				sess.subsDir = filepath.Join(s.dataDir, "subs", sessionID)
			}
			sess.mu.Unlock()
			log.Printf("subtitle job %s ready: engine=%s applied=%v provider=%q cache=%v",
				sessionID, res.Engine, res.Applied, res.Provider, res.FromCache)
		}
	}()
}

// handleStream serves the video with HTTP Range support. For DirectPlay it
// streams the raw (partial) file; for Remux it pipes an on-the-fly fragmented
// MP4 from ffmpeg (browser-native codecs, wrong container). Transcode mode is
// served separately via HLS, so it should not reach here.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.sessions.get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "session not found")
		return
	}

	// Register this read so teardown waits for it to finish before closing the
	// reader/torrent (prevents a GC sweep from freeing the reader mid-read).
	if !sess.beginServe() {
		writeErr(w, http.StatusGone, "session closed")
		return
	}
	defer sess.endServe()

	// A Transcode-mode session must be played via /api/hls, not here. If one
	// reaches this handler, point the client at the playlist rather than
	// serving raw, unplayable bytes.
	if sess.mode == transcode.Transcode && sess.hlsDir != "" {
		http.Redirect(w, r, "/api/hls/"+id+"/index.m3u8", http.StatusTemporaryRedirect)
		return
	}

	if sess.mode == transcode.Remux && s.trans.Available() && sess.videoPath != "" {
		// Fragmented MP4 is a live pipe (not Range-seekable); stream it whole.
		// The browser buffers progressively.
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Accept-Ranges", "none")
		if err := s.trans.RemuxToWriter(r.Context(), sess.videoPath, sess.mediaInfo, w); err != nil {
			log.Printf("remux stream %s ended: %v", id, err)
		}
		return
	}

	// Direct play: give this request its own independent reader so concurrent
	// Range connections never contend on a shared seek offset.
	reader, independent, err := sess.stream.OpenReader()
	if err != nil {
		writeErr(w, http.StatusBadGateway, "open reader: "+err.Error())
		return
	}
	if independent {
		defer reader.Close()
	}
	// ServeContent parses Range, emits 206, and seeks via the ReadSeeker —
	// which triggers the engine's piece reprioritization for responsive scrub.
	http.ServeContent(w, r, sess.stream.Name, time.Now(), reader)
}

// waitForHLSReady polls until the playlist and at least one .ts segment exist,
// or the timeout elapses. Returns true if ready.
func waitForHLSReady(playlist, outDir string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(playlist); err == nil {
			if segs, _ := filepath.Glob(filepath.Join(outDir, "*.ts")); len(segs) > 0 {
				return true
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// handleHLS serves the HLS playlist and .ts segments produced by a transcode.
func (s *Server) handleHLS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	file := r.PathValue("file")
	sess, ok := s.sessions.get(id)
	if !ok || sess.hlsDir == "" {
		writeErr(w, http.StatusNotFound, "no HLS session")
		return
	}
	// Prevent path traversal; only serve the playlist and segment files.
	clean := filepath.Base(file)
	full := filepath.Join(sess.hlsDir, clean)
	if strings.HasSuffix(clean, ".m3u8") {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	} else if strings.HasSuffix(clean, ".ts") {
		w.Header().Set("Content-Type", "video/mp2t")
	}
	http.ServeFile(w, r, full)
}

func (s *Server) handleProgress(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.sessions.get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "session not found")
		return
	}
	var p float64
	if sess.stream.Progress != nil {
		p = sess.stream.Progress()
	}
	writeJSON(w, http.StatusOK, map[string]float64{"progress": p})
}

func (s *Server) handleSubtitle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.sessions.get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "session not found")
		return
	}
	sess.mu.Lock()
	sub := sess.subtitle
	sess.mu.Unlock()
	if sub == "" {
		// Not ready yet. 202 (not 404) signals "pending, keep polling" so the
		// client injects the <track> once it becomes available, rather than
		// giving up on a single failed fetch.
		if s.subs == nil {
			writeErr(w, http.StatusNotFound, "subtitles disabled")
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
		return
	}
	http.ServeFile(w, r, sub)
}

// --- SPA / static ---

func (s *Server) spaHandler() http.Handler {
	fs := http.FileServer(http.Dir(s.webDir))
	root, _ := filepath.Abs(s.webDir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Resolve the requested path and ensure it stays within webDir before
		// touching the filesystem (defense-in-depth around the hand-rolled
		// os.Stat / index.html fallback; the stdlib FileServer also guards).
		p := filepath.Join(root, filepath.Clean("/"+r.URL.Path))
		if !strings.HasPrefix(p, root) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// Serve the file if it exists; otherwise fall back to index.html so
		// client-side routing works on deep links.
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			fs.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(root, "index.html"))
	})
}

// --- candidate cache ---

func (s *Server) cacheCandidates(key string, c []source.Candidate) {
	s.candMu.Lock()
	s.candCache[key] = c
	s.candMu.Unlock()
}

func (s *Server) getCachedCandidates(key string) ([]source.Candidate, bool) {
	s.candMu.Lock()
	defer s.candMu.Unlock()
	c, ok := s.candCache[key]
	return c, ok
}

func cacheKey(tmdbID int, mType string, season, episode int) string {
	return mType + ":" + strconv.Itoa(tmdbID) + ":" + strconv.Itoa(season) + ":" + strconv.Itoa(episode)
}

// --- helpers ---

func mediaType(t string) source.MediaType {
	if t == "tv" || t == "episode" {
		return source.MediaEpisode
	}
	return source.MediaMovie
}

func tmdbAPIType(t string) string {
	if t == "tv" || t == "episode" {
		return "tv"
	}
	return "movie"
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// videoPathOf returns the on-disk path backing a stream, if the engine exposes
// one. The qBittorrent engine backs Reader with an *os.File; the anacrolix
// engine's reader implements Name() returning "" (no stable path), in which
// case audio extraction / probing is skipped gracefully.
func videoPathOf(st *source.Stream) string {
	if st == nil || st.Reader == nil {
		return ""
	}
	if f, ok := st.Reader.(interface{ Name() string }); ok {
		return f.Name()
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}
