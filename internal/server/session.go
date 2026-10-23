package server

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/0xisalive/streamr/internal/source"
	"github.com/0xisalive/streamr/internal/transcode"
)

// session holds a live stream plus bookkeeping for idle garbage collection.
// The single most important operational lesson from comparable projects is
// that torrents accumulate and leak resources; we track last-access per session
// and tear down anything idle past the timeout.
type session struct {
	id        string
	stream    *source.Stream
	candidate source.Candidate
	subtitle  string // path of the aligned/generated WebVTT, once ready

	// delivery
	mode      transcode.Mode
	mediaInfo transcode.MediaInfo
	videoPath string // on-disk path for probe/remux/transcode (if available)
	hlsDir    string // transcode output dir, if mode == Transcode
	hlsStop   func() // stops the background HLS transcode
	subsDir   string // per-session subtitle working dir (cleaned on teardown)

	lastAccess time.Time
	mu         sync.Mutex // guards lastAccess + subtitle

	// serving tracks in-flight stream reads so teardown can wait for them to
	// drain before closing the underlying reader/torrent (prevents a GC sweep
	// from freeing the reader mid-ServeContent). closed is set under servingMu
	// once teardown has begun, so new reads are refused.
	servingMu sync.Mutex
	serving   sync.WaitGroup
	closed    bool
}

// beginServe registers an in-flight read. It returns false if the session is
// already being torn down, in which case the caller must not touch the stream.
func (s *session) beginServe() bool {
	s.servingMu.Lock()
	defer s.servingMu.Unlock()
	if s.closed {
		return false
	}
	s.serving.Add(1)
	return true
}

func (s *session) endServe() { s.serving.Done() }

// beginClose marks the session closed (refusing new reads) and waits for all
// in-flight reads to finish. Returns false if already closed by another caller.
func (s *session) beginClose() bool {
	s.servingMu.Lock()
	if s.closed {
		s.servingMu.Unlock()
		return false
	}
	s.closed = true
	s.servingMu.Unlock()
	s.serving.Wait() // drain in-flight reads before teardown proceeds
	return true
}

func (s *session) touch() {
	s.mu.Lock()
	s.lastAccess = time.Now()
	s.mu.Unlock()
}

func (s *session) idleFor() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastAccess)
}

// sessionManager owns all live streaming sessions and runs the idle sweeper.
type sessionManager struct {
	mu          sync.RWMutex
	sessions    map[string]*session
	idleTimeout time.Duration
	stop        chan struct{}
}

func newSessionManager(idleTimeout time.Duration) *sessionManager {
	m := &sessionManager{
		sessions:    make(map[string]*session),
		idleTimeout: idleTimeout,
		stop:        make(chan struct{}),
	}
	go m.sweepLoop()
	return m
}

func (m *sessionManager) add(s *session) {
	s.touch()
	m.mu.Lock()
	m.sessions[s.id] = s
	m.mu.Unlock()
}

func (m *sessionManager) get(id string) (*session, bool) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if ok {
		s.touch()
	}
	return s, ok
}

func (m *sessionManager) remove(id string) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if !ok {
		return
	}
	// Refuse new reads and wait for in-flight ones to drain before closing the
	// underlying reader/torrent — otherwise a sweep could free the reader out
	// from under an active ServeContent (use-after-free).
	if !s.beginClose() {
		return // already being torn down
	}
	// Stop any background transcode first, then tear down the torrent/stream.
	if s.hlsStop != nil {
		s.hlsStop()
	}
	if s.stream != nil && s.stream.Close != nil {
		_ = s.stream.Close()
	}
	// Clean up per-session temp dirs (HLS segments, extracted audio, VTTs) so
	// disk doesn't grow unbounded across sessions.
	for _, d := range []string{s.hlsDir, s.subsDir} {
		if d != "" {
			_ = os.RemoveAll(d)
		}
	}
}

// sweepLoop periodically destroys sessions that have been idle too long,
// freeing the underlying torrent and disk.
func (m *sessionManager) sweepLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			m.sweep()
		}
	}
}

func (m *sessionManager) sweep() {
	var expired []string
	m.mu.RLock()
	for id, s := range m.sessions {
		if s.idleFor() > m.idleTimeout {
			expired = append(expired, id)
		}
	}
	m.mu.RUnlock()
	// Tear down expired sessions concurrently — a slow torrent drop or ffmpeg
	// kill on one session must not stall cleanup of the others.
	var wg sync.WaitGroup
	for _, id := range expired {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			m.remove(id)
		}(id)
	}
	wg.Wait()
}

// shutdown stops the sweeper and tears down every session in parallel.
func (m *sessionManager) shutdown(_ context.Context) {
	close(m.stop)
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			m.remove(id)
		}(id)
	}
	wg.Wait()
}
