package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/klauspost/fox/internal/optimize"
)

const (
	flushInterval = 100 * time.Millisecond
	keepAlive     = 15 * time.Second
)

// client collects which jobs changed, so a slow browser never blocks the engine.
type client struct {
	mu      sync.Mutex
	changed map[int64]struct{}
	removed []int64
	resync  bool
	notify  chan struct{}
}

func newClient() *client {
	return &client{changed: map[int64]struct{}{}, resync: true, notify: make(chan struct{}, 1)}
}

func (c *client) push(e optimize.Event) {
	c.mu.Lock()
	c.changed[e.ID] = struct{}{}
	c.mu.Unlock()
	c.wake()
}

// requestSnapshot makes the next flush send the full job list, e.g. after jobs were removed.
func (c *client) requestSnapshot() {
	c.mu.Lock()
	c.resync = true
	c.mu.Unlock()
	c.wake()
}

// drop tells the browser to forget jobs the server removed.
func (c *client) drop(ids []int64) {
	c.mu.Lock()
	c.removed = append(c.removed, ids...)
	c.mu.Unlock()
	c.wake()
}

func (c *client) wake() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

func (c *client) take() (snapshot bool, ids map[int64]struct{}, removed []int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	snapshot, c.resync = c.resync, false
	ids, c.changed = c.changed, map[int64]struct{}{}
	removed, c.removed = c.removed, nil
	return snapshot, ids, removed
}

// stats are the jobs queued and running now, and the session totals of finished jobs,
// which are kept when jobs leave the list.
type stats struct {
	Queued  int `json:"queued"`
	Running int `json:"running"`
	Done    int `json:"done"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
	// Original and Size cover done jobs only.
	Original int64 `json:"original"`
	Size     int64 `json:"size"`
	// Elapsed is the wall time during which jobs were queued or running.
	Elapsed time.Duration `json:"elapsed"`
}

func (s *server) addClient(c *client) {
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.connected = true
	s.mu.Unlock()
}

func (s *server) dropClient(c *client) {
	s.mu.Lock()
	delete(s.clients, c)
	s.lastGone = time.Now()
	s.mu.Unlock()
}

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")

	c := newClient()
	unsubscribe := s.engine.Subscribe(c.push)
	defer unsubscribe()
	s.addClient(c)
	defer s.dropClient(c)
	c.wake()

	send := func(event string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		return err
	}
	if _, err := fmt.Fprint(w, "retry: 2000\n\n"); err != nil {
		return
	}

	ping := time.NewTicker(keepAlive)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case <-c.notify:
			snapshot, changed, removed := c.take()
			jobs := s.engine.Jobs()
			var err error
			if snapshot {
				if jobs == nil {
					jobs = []optimize.Event{}
				}
				err = send("snapshot", jobs)
			} else {
				// Sending the engine's current state, rather than the event that triggered the change,
				// never resurrects a job removed meanwhile and never sends a state older than the last snapshot.
				for _, e := range jobs {
					if _, ok := changed[e.ID]; ok && err == nil {
						err = send("job", e)
					}
				}
				if len(removed) > 0 && err == nil {
					err = send("remove", removed)
				}
			}
			if err != nil || send("stats", s.stats(jobs)) != nil || rc.Flush() != nil {
				return
			}
			// Throttle; anything arriving meanwhile is coalesced and sent on the next round.
			select {
			case <-time.After(flushInterval):
			case <-r.Context().Done():
				return
			}
		}
	}
}

func (s *server) resyncClients() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		c.requestSnapshot()
	}
}
