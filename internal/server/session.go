package server

import (
	"context"
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

	lastAccess time.Time
	mu         sync.Mutex // guards lastAccess + subtitle
	serveMu    sync.Mutex // serializes ServeContent (seek+read) on the shared reader
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
	// Stop any background transcode first, then tear down the torrent/stream.
	if s.hlsStop != nil {
		s.hlsStop()
	}
	if s.stream != nil && s.stream.Close != nil {
		_ = s.stream.Close()
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
