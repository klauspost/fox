// Package server serves the browser UI on localhost.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/fox/internal/config"
	"github.com/klauspost/fox/internal/optimize"
	"github.com/klauspost/fox/internal/tools"
)

//go:embed web
var webFS embed.FS

// Engine is the part of *optimize.Engine the server needs.
type Engine interface {
	Add(path string, upload bool) []int64
	Subscribe(fn func(optimize.Event)) (unsubscribe func())
	Jobs() []optimize.Event
	Job(id int64) (optimize.Event, bool)
	Cancel(id int64)
	Remove(id int64)
	Settings() *config.Settings
	SetSettings(s *config.Settings)
}

var _ Engine = (*optimize.Engine)(nil)

// idleExit is how long the server keeps running after the last browser tab is gone.
const idleExit = 30 * time.Second

type toolInfo struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Available bool   `json:"available"`
}

type server struct {
	engine    Engine
	tools     func() []toolInfo
	token     string
	hosts     []string
	uploadDir string
	dialog    sync.Mutex
	saving    sync.Mutex

	mu        sync.Mutex
	uploads   map[int64]*upload
	clients   map[*client]struct{}
	connected bool
	lastGone  time.Time

	sess session
}

type upload struct {
	path    string
	fetched bool // the result was downloaded, e.g. to write it back
}

func newServer(e Engine, tools func() []toolInfo) (*server, error) {
	dir, err := os.MkdirTemp("", "fox-uploads-")
	if err != nil {
		return nil, err
	}
	s := &server{
		engine:    e,
		tools:     tools,
		token:     rand.Text(),
		uploadDir: dir,
		uploads:   map[int64]*upload{},
		clients:   map[*client]struct{}{},
		sess:      session{open: map[int64]struct{}{}, evict: make(chan struct{}, 1)},
	}
	// Subscribed before any job exists, so every job is seen from its first event.
	e.Subscribe(s.observe)
	return s, nil
}

// Run serves the UI until ctx is cancelled, or until all browser tabs have been closed for a while and no jobs remain.
func Run(ctx context.Context, e Engine, openBrowser bool) error {
	s, err := newServer(e, toolLister(e))
	if err != nil {
		return err
	}
	defer os.RemoveAll(s.uploadDir)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.setPort(ln.Addr().(*net.TCPAddr).Port)
	u := fmt.Sprintf("http://%s/%s/", s.hosts[0], s.token)

	// Cancelling every request context on exit ends event streams and closes open file dialogs.
	reqCtx, stop := context.WithCancel(ctx)
	defer stop()
	srv := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return reqCtx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	go s.evictLoop(reqCtx)

	fmt.Printf("fox is running at %s\nPress Ctrl+C to quit.\n", u)
	if openBrowser {
		cleanup, err := launch(u)
		if err != nil {
			fmt.Fprintln(os.Stderr, "could not open browser:", err)
		}
		defer cleanup()
	}

	idle := make(chan struct{})
	go func() { s.waitIdle(reqCtx, idleExit, time.Second); close(idle) }()
	select {
	case <-reqCtx.Done():
	case <-idle:
	case err = <-errc:
	}
	stop()
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

func toolLister(e Engine) func() []toolInfo {
	// A private resolver is fine here: Available never extracts anything.
	resolver := sync.OnceValue(tools.NewResolver)
	return func() []toolInfo {
		r := resolver()
		r.SetOverrides(e.Settings().ToolPaths)
		list := make([]toolInfo, 0, len(tools.All))
		for name, t := range tools.All {
			list = append(list, toolInfo{Name: name, URL: t.URL, Available: r.Available(name)})
		}
		slices.SortFunc(list, func(a, b toolInfo) int { return strings.Compare(a.Name, b.Name) })
		return list
	}
}

func (s *server) setPort(port int) {
	s.hosts = []string{fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf("localhost:%d", port)}
}

func (s *server) handler() http.Handler {
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("POST /api/pick", s.pick)
	mux.HandleFunc("POST /api/add", s.add)
	mux.HandleFunc("POST /api/upload", s.upload)
	mux.HandleFunc("GET /api/download/{id}", s.download)
	mux.HandleFunc("POST /api/cancel", s.cancel)
	mux.HandleFunc("POST /api/remove", s.remove)
	mux.HandleFunc("POST /api/clear", s.clear)
	mux.HandleFunc("POST /api/stats/reset", s.resetStats)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	mux.HandleFunc("GET /api/tools", s.listTools)
	inner := http.StripPrefix("/"+s.token, mux)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		// The token is in every URL; never leak it to the external links in the footer.
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")

		// Rejecting foreign Host headers defeats DNS rebinding.
		if !slices.Contains(s.hosts, r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		tok, _, found := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
			http.NotFound(w, r)
			return
		}
		if !found {
			http.Redirect(w, r, "/"+s.token+"/", http.StatusFound)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// waitIdle returns when ctx is done, or once a client has connected, all clients have been gone for d and the engine is idle.
func (s *server) waitIdle(ctx context.Context, d, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			gone := s.connected && len(s.clients) == 0 && time.Since(s.lastGone) >= d
			s.mu.Unlock()
			if gone && !s.busy() {
				return
			}
		}
	}
}

func (s *server) busy() bool {
	return slices.ContainsFunc(s.engine.Jobs(), func(e optimize.Event) bool {
		return e.State == optimize.Queued || e.State == optimize.Running
	})
}

// Variables so tests can shorten the TTL and cover both launch modes on any OS.
var (
	// launchTTL is how long the redirect file outlives the browser launch.
	launchTTL = 30 * time.Second
	// Other users cannot read command lines on Windows, and its shell decodes %-escaped
	// file URLs as ANSI, so a redirect file in a non-ASCII profile could not be opened.
	redirectFile = runtime.GOOS != "windows"
)

// launch opens u through a private redirect file, because the browser command line,
// which other local users can read on Linux and macOS, must not carry the token.
// The returned cleanup removes the file; it is also removed after launchTTL.
func launch(u string) (cleanup func(), err error) {
	if !redirectFile {
		return func() {}, browse(u)
	}
	dir, err := os.MkdirTemp("", "fox-open-")
	if err != nil {
		return func() {}, err
	}
	rm := func() { os.RemoveAll(dir) }
	p := filepath.Join(dir, "fox.html")
	page := fmt.Sprintf(`<!doctype html><meta charset="utf-8"><meta name="referrer" content="no-referrer">`+
		`<meta http-equiv="refresh" content="0;url=%[1]s"><title>fox</title>`+
		`<script>location.replace(%[1]q)</script><a href="%[1]s">Open fox</a>`, u)
	if err = os.WriteFile(p, []byte(page), 0o600); err == nil {
		err = browse(p)
	}
	if err != nil {
		rm()
		return func() {}, err
	}
	t := time.AfterFunc(launchTTL, rm)
	return func() { t.Stop(); rm() }, nil
}

// browse is a variable so tests can stub it.
var browse = func(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
