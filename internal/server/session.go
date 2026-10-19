package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/klauspost/fox/internal/optimize"
)

// maxFinished bounds the finished jobs kept in the list; a variable so tests can lower it.
var maxFinished = 1000

const evictInterval = 100 * time.Millisecond

// session holds the totals since start or the last reset. They outlive the jobs in the list.
type session struct {
	mu    sync.Mutex
	st    stats              // Queued and Running are unused
	open  map[int64]struct{} // jobs seen queued or running and not counted yet
	since time.Time          // start of the current busy period
	// finished estimates the finished jobs the engine holds, so the list is only scanned when it may be too long.
	finished int
	evict    chan struct{}
}

func final(s optimize.State) bool {
	return s == optimize.Done || s == optimize.Skipped || s == optimize.Failed
}

// observe counts a job when it leaves the queued/running set. It runs inside the engine's event
// delivery, which may happen inside engine.Add called with s.mu held, so it must not take s.mu.
func (s *server) observe(e optimize.Event) {
	ss := &s.sess
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if !final(e.State) {
		if len(ss.open) == 0 {
			ss.since = time.Now()
		}
		ss.open[e.ID] = struct{}{}
		return
	}
	// Counting only jobs seen active means a repeated final event can never count twice.
	if _, ok := ss.open[e.ID]; !ok {
		return
	}
	delete(ss.open, e.ID)
	if len(ss.open) == 0 {
		ss.st.Elapsed += time.Since(ss.since)
	}
	switch e.State {
	case optimize.Done:
		ss.st.Done++
		ss.st.Original += e.Original
		ss.st.Size += e.Size
	case optimize.Skipped:
		ss.st.Skipped++
	case optimize.Failed:
		ss.st.Failed++
	}
	if ss.finished++; ss.finished > maxFinished {
		ss.signal()
	}
}

// signal asks the evict loop to recount the finished jobs and trim them.
func (ss *session) signal() {
	select {
	case ss.evict <- struct{}{}:
	default:
	}
}

// stats returns the session totals with the queued and running counts of jobs.
func (s *server) stats(jobs []optimize.Event) stats {
	ss := &s.sess
	ss.mu.Lock()
	st := ss.st
	if len(ss.open) > 0 {
		st.Elapsed += time.Since(ss.since)
	}
	ss.mu.Unlock()
	for _, j := range jobs {
		switch j.State {
		case optimize.Queued:
			st.Queued++
		case optimize.Running:
			st.Running++
		}
	}
	return st
}

func (s *server) resetStats(w http.ResponseWriter, r *http.Request) {
	s.sess.mu.Lock()
	s.sess.st = stats{}
	s.sess.since = time.Now()
	s.sess.mu.Unlock()
	s.mu.Lock()
	for c := range s.clients {
		c.wake()
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) evictLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.sess.evict:
			s.evictFinished()
		}
		// Each round copies the whole job list, which may hold a huge queue; the list may
		// briefly exceed maxFinished by the jobs finishing meanwhile.
		select {
		case <-ctx.Done():
			return
		case <-time.After(evictInterval):
		}
	}
}

// evictFinished removes the oldest finished jobs beyond maxFinished. Optimized uploads that were
// never downloaded are kept, since removing them deletes the only copy of the result; their number
// is bounded by what the user dropped.
func (s *server) evictFinished() {
	jobs := s.engine.Jobs()
	n := 0
	for _, j := range jobs {
		if final(j.State) {
			n++
		}
	}
	var ids []int64
	s.mu.Lock()
	for _, j := range jobs {
		if n-len(ids) <= maxFinished {
			break
		}
		u := s.uploads[j.ID]
		pending := u != nil && !u.fetched && j.State == optimize.Done && j.Size < j.Original
		if final(j.State) && !pending {
			ids = append(ids, j.ID)
		}
	}
	s.mu.Unlock()
	// Jobs finishing after Jobs() was taken signal again, so overwriting their increments is fine.
	s.sess.mu.Lock()
	s.sess.finished = n - len(ids)
	s.sess.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	for _, id := range ids {
		s.engine.Remove(id)
	}
	s.dropUploads()
	// A remove event instead of a snapshot: resending a long queue on every finished job would be costly.
	s.mu.Lock()
	for c := range s.clients {
		c.drop(ids)
	}
	s.mu.Unlock()
}
