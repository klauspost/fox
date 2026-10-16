package optimize

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/fox/internal/config"
	"github.com/klauspost/fox/internal/detect"
	"github.com/klauspost/fox/internal/pipeline"
	"github.com/klauspost/fox/internal/tools"
)

// resolver is the part of *tools.Resolver the engine uses; tests substitute a fake.
type resolver interface {
	Command(name string) ([]string, error)
	Available(name string) bool
	SetOverrides(map[string][]string)
}

// hooks are the engine's external dependencies, replaceable in tests.
type hooks struct {
	detect func(path string, jsExt []string) (detect.Format, error)
	steps  func(f detect.Format, path string, s *config.Settings) ([]pipeline.Step, string)
	cache  string // cache file; "" disables the cache
	tmp    string // base temp dir; "" means tmpRoot()
}

type job struct {
	ev     Event
	key    string
	ctx    context.Context
	cancel context.CancelFunc
}

type subscriber struct {
	id int
	fn func(Event)
}

// Engine owns the job queue and worker pool.
type Engine struct {
	res     resolver
	h       hooks
	cache   cache
	tmpBase func() (string, error)

	mu       sync.Mutex
	idle     *sync.Cond
	s        *config.Settings
	lastID   int64
	jobs     map[int64]*job
	order    []int64
	queue    []*job
	active   map[string]*job // queued or running, by path key
	running  int
	closed   bool
	subs     []subscriber
	lastSub  int
	pending  []Event
	flushing bool
}

// New creates an engine with the given settings. Workers start immediately.
func New(r *tools.Resolver, s *config.Settings) *Engine {
	h := hooks{detect: detect.Detect, steps: pipeline.For}
	if dir, err := config.Dir(); err == nil {
		h.cache = filepath.Join(dir, "cache")
	}
	return newEngine(r, s, h)
}

func newEngine(r resolver, s *config.Settings, h hooks) *Engine {
	e := &Engine{
		res:    r,
		h:      h,
		cache:  cache{path: h.cache},
		s:      s.Clone(),
		jobs:   map[int64]*job{},
		active: map[string]*job{},
	}
	e.idle = sync.NewCond(&e.mu)
	e.tmpBase = sync.OnceValues(func() (string, error) {
		dir := h.tmp
		if dir == "" {
			dir = tmpRoot()
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		if err := ensurePrivate(dir); err != nil {
			return "", err
		}
		// Left behind by killed fox processes. Live jobs touch their dir at every step, and other
		// fox processes may share this dir, so only old ones are removed.
		es, _ := os.ReadDir(dir)
		for _, d := range es {
			if fi, err := d.Info(); err == nil && time.Since(fi.ModTime()) > 7*24*time.Hour {
				os.RemoveAll(filepath.Join(dir, d.Name()))
			}
		}
		return shortPath(dir), nil
	})
	r.SetOverrides(e.s.ToolPaths)
	return e
}

// SetSettings replaces the settings used for jobs started from now on.
func (e *Engine) SetSettings(s *config.Settings) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.s = s.Clone()
	e.res.SetOverrides(e.s.ToolPaths)
	e.dispatchLocked()
}

// Settings returns a copy of the current settings.
func (e *Engine) Settings() *config.Settings {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.s.Clone()
}

// Add queues a file, or all files below a directory. Paths already queued or running are ignored.
// upload marks files received through the browser; they live in a temp dir owned by the caller.
func (e *Engine) Add(path string, upload bool) []int64 {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	paths := []string{path}
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		jsExt := e.Settings().Web.JSExtensions
		paths = nil
		filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
			// The root is checked too: the CLI expands "dir/*" into explicit paths that include .git and fox-tools.
			case d.IsDir() && skipDir(p):
				return filepath.SkipDir
			case d.Type().IsRegular() && detect.Known(p, jsExt):
				paths = append(paths, p)
			}
			return nil
		})
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	var ids []int64
	for _, p := range paths {
		k := pathKey(p)
		if e.active[k] != nil {
			continue
		}
		e.lastID++
		ctx, cancel := context.WithCancel(context.Background())
		j := &job{key: k, ctx: ctx, cancel: cancel}
		e.jobs[e.lastID] = j
		e.order = append(e.order, e.lastID)
		e.active[k] = j
		e.queue = append(e.queue, j)
		e.setLocked(j, Event{ID: e.lastID, Path: p, State: Queued, Upload: upload})
		ids = append(ids, e.lastID)
	}
	e.dispatchLocked()
	e.mu.Unlock()
	e.flush()
	return ids
}

// skipDirs hold tool bundles, version control data and content-addressed caches,
// whose files are verified by checksum and break when optimized. Yarn checks its cached zips
// against yarn.lock; node_modules/.pnpm files are hard links into pnpm's shared store.
var skipDirs = []string{"fox-tools", ".git", ".svn", ".hg", ".bzr", "_cacache", "__pycache__", ".yarn", ".pnpm"}

func skipDir(p string) bool {
	name := filepath.Base(p)
	if strings.EqualFold(name, ".cache") && strings.EqualFold(filepath.Base(filepath.Dir(p)), "node_modules") {
		return true
	}
	return slices.ContainsFunc(skipDirs, func(s string) bool { return strings.EqualFold(s, name) })
}

func pathKey(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

// Subscribe registers fn to receive every event. fn is called synchronously and must not block.
func (e *Engine) Subscribe(fn func(Event)) (unsubscribe func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastSub++
	id := e.lastSub
	e.subs = append(e.subs, subscriber{id, fn})
	return func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.subs = slices.DeleteFunc(e.subs, func(s subscriber) bool { return s.id == id })
	}
}

// Jobs returns the latest event of every known job, in insertion order.
func (e *Engine) Jobs() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Event, 0, len(e.order))
	for _, id := range e.order {
		out = append(out, e.jobs[id].ev)
	}
	return out
}

// Job returns the latest event for id.
func (e *Engine) Job(id int64) (Event, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if j := e.jobs[id]; j != nil {
		return j.ev, true
	}
	return Event{}, false
}

// Cancel stops a queued or running job. id 0 cancels everything.
func (e *Engine) Cancel(id int64) {
	e.mu.Lock()
	e.queue = slices.DeleteFunc(e.queue, func(j *job) bool {
		if id != 0 && j.ev.ID != id {
			return false
		}
		delete(e.active, j.key)
		ev := j.ev
		ev.State, ev.Message = Skipped, "cancelled"
		e.setLocked(j, ev)
		return true
	})
	for _, j := range e.jobs {
		if id == 0 || j.ev.ID == id {
			j.cancel()
		}
	}
	e.idle.Broadcast()
	e.mu.Unlock()
	e.flush()
}

// Remove forgets a finished job. id 0 removes all finished jobs.
func (e *Engine) Remove(id int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.order = slices.DeleteFunc(e.order, func(i int64) bool {
		if id != 0 && i != id {
			return false
		}
		switch e.jobs[i].ev.State {
		case Done, Skipped, Failed:
			delete(e.jobs, i)
			return true
		}
		return false
	})
}

// Wait blocks until no jobs are queued or running.
func (e *Engine) Wait() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for e.running > 0 || len(e.queue) > 0 || e.flushing || len(e.pending) > 0 {
		e.idle.Wait()
	}
}

// Close cancels all jobs and stops the workers.
func (e *Engine) Close() {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	e.Cancel(0)
	e.Wait()
}

func (e *Engine) dispatchLocked() {
	n := e.s.Workers
	if n == 0 {
		n = runtime.NumCPU()
	}
	for e.running < n && len(e.queue) > 0 {
		j := e.queue[0]
		e.queue = e.queue[1:]
		e.running++
		go e.run(j, j.ev, e.s.Clone())
	}
}

func (e *Engine) run(j *job, ev Event, s *config.Settings) {
	t := &task{e: e, j: j, ev: ev, s: s}
	ev = t.process(j.ctx)
	j.cancel()
	e.mu.Lock()
	e.running--
	delete(e.active, j.key)
	e.setLocked(j, ev)
	e.dispatchLocked()
	e.idle.Broadcast()
	e.mu.Unlock()
	e.flush()
}

func (e *Engine) publish(j *job, ev Event) {
	e.mu.Lock()
	e.setLocked(j, ev)
	e.mu.Unlock()
	e.flush()
}

func (e *Engine) setLocked(j *job, ev Event) {
	j.ev = ev
	e.pending = append(e.pending, ev)
}

// flush delivers pending events outside the lock. Only one goroutine delivers at a time, which
// keeps events in the order their state changed and lets subscribers call back into the engine.
func (e *Engine) flush() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.flushing {
		return
	}
	e.flushing = true
	for len(e.pending) > 0 {
		evs, subs := e.pending, slices.Clone(e.subs)
		e.pending = nil
		e.mu.Unlock()
		for _, ev := range evs {
			for _, s := range subs {
				s.fn(ev)
			}
		}
		e.mu.Lock()
	}
	e.flushing = false
	e.idle.Broadcast()
}
