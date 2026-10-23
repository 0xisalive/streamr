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
	"sync"
	"time"

	"github.com/0xisalive/streamr/internal/config"
	"github.com/0xisalive/streamr/internal/discovery"
	"github.com/0xisalive/streamr/internal/source"
	"github.com/0xisalive/streamr/internal/subtitle"
)

// Server holds all wired dependencies.
type Server struct {
	cfg      config.Config
	tmdb     *discovery.Client
	resolver *source.Resolver
	engine   source.Engine
	subs     *subtitle.Coordinator // may be nil if no subtitle providers configured
	sessions *sessionManager
	webDir   string

	// candidate cache per (tmdb,type,season,episode) so /play can reference a
	// candidate index returned by a prior /sources call.
	candMu    sync.Mutex
	candCache map[string][]source.Candidate
}

// New builds the server. engine and subs may be nil; the API still serves
// discovery/search without them.
func New(cfg config.Config, tmdb *discovery.Client, resolver *source.Resolver, engine source.Engine, subs *subtitle.Coordinator) *Server {
	idle := time.Duration(cfg.SessionIdleTimeoutMinutes) * time.Minute
	return &Server{
		cfg:       cfg,
		tmdb:      tmdb,
		resolver:  resolver,
		engine:    engine,
		subs:      subs,
		sessions:  newSessionManager(idle),
		webDir:    cfg.WebDir,
		candCache: make(map[string][]source.Candidate),
	}
}

// Handler returns the configured http.Handler (API + static frontend).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/sources", s.handleSources)
	mux.HandleFunc("POST /api/play", s.handlePlay)
	mux.HandleFunc("GET /api/stream/{id}", s.handleStream)
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
	chosen := cands[req.CandidateIndex]

	// Opening can take a moment (metadata fetch); cap it.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	stream, err := s.engine.Open(ctx, chosen)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "failed to open stream: "+err.Error())
		return
	}

	id := newID()
	sess := &session{id: id, stream: stream}
	s.sessions.add(sess)

	// Subtitle alignment runs asynchronously so playback can begin immediately.
	// The client polls /api/subtitle/{id}; when the aligned WebVTT is ready the
	// session is updated and the track becomes available.
	if s.subs != nil {
		s.startSubtitleJob(id, chosen, req)
	}

	resp := map[string]string{
		"session_id":   id,
		"stream_url":   "/api/stream/" + id,
		"subtitle_url": "/api/subtitle/" + id,
	}
	writeJSON(w, http.StatusOK, resp)
}

// startSubtitleJob fetches + aligns a subtitle for the session in the
// background. videoPath is derived from the stream's on-disk file (the
// qBittorrent engine backs Stream.Reader with an *os.File).
func (s *Server) startSubtitleJob(sessionID string, chosen source.Candidate, req playRequest) {
	sess, ok := s.sessions.get(sessionID)
	if !ok {
		return
	}
	videoPath := ""
	if f, ok := sess.stream.Reader.(interface{ Name() string }); ok {
		videoPath = f.Name() // *os.File.Name() returns its path
	}
	if videoPath == "" {
		return
	}

	imdb := ""
	if s.tmdb.Enabled() && req.TmdbID != 0 {
		if id, err := s.tmdb.ExternalIDs(context.Background(), req.TmdbID, tmdbAPIType(req.Type)); err == nil {
			imdb = id
		}
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		res, err := s.subs.Prepare(ctx, sessionID, videoPath, subtitle.FetchRequest{
			Title:   chosen.Title,
			ImdbID:  imdb,
			Lang:    "en",
			Season:  req.Season,
			Episode: req.Episode,
		})
		if err != nil {
			log.Printf("subtitle job %s failed: %v", sessionID, err)
			return
		}
		if sess, ok := s.sessions.get(sessionID); ok {
			sess.mu.Lock()
			sess.subtitle = res.VTTPath
			sess.mu.Unlock()
			log.Printf("subtitle job %s ready: engine=%s applied=%v provider=%s",
				sessionID, res.Pipeline.Engine, res.Pipeline.Applied, res.Provider)
		}
	}()
}

// handleStream serves the (partial) video file with HTTP Range support.
// http.ServeContent does all the heavy lifting: it parses the Range header,
// emits 206 Partial Content, and uses the io.ReadSeeker to serve the requested
// window. Seeking triggers the engine's piece re-prioritization via Seek.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := s.sessions.get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "session not found")
		return
	}
	st := sess.stream
	// ServeContent needs an io.ReadSeeker; our ReadSeekraleCloser satisfies it.
	http.ServeContent(w, r, st.Name, time.Now(), st.Reader)
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
	if !ok || sess.subtitle == "" {
		writeErr(w, http.StatusNotFound, "no subtitle ready")
		return
	}
	http.ServeFile(w, r, sess.subtitle)
}

// --- SPA / static ---

func (s *Server) spaHandler() http.Handler {
	fs := http.FileServer(http.Dir(s.webDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve the file if it exists; otherwise fall back to index.html so
		// client-side routing works on deep links.
		p := filepath.Join(s.webDir, filepath.Clean(r.URL.Path))
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			fs.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(s.webDir, "index.html"))
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
