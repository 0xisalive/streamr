package server

import (
	"context"
	"sync"
	"time"

	"github.com/0xisalive/streamr/internal/source"
)

// session holds a live stream plus bookkeeping for idle garbage collection.
// The single most important operational lesson from comparable projects is
// that torrents accumulate and leak resources; we track last-access per session
// and tear down anything idle past the timeout.
type session struct {
	id         string
	stream     *source.Stream
	subtitle   string // path/URL of the aligned subtitle, if any
	lastAccess time.Time
	mu         sync.Mutex
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
	if ok && s.stream != nil && s.stream.Close != nil {
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
	for _, id := range expired {
		m.remove(id)
	}
}

// shutdown stops the sweeper and tears down every session.
func (m *sessionManager) shutdown(_ context.Context) {
	close(m.stop)
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.remove(id)
	}
}
