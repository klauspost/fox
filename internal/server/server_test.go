package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/fox/internal/config"
	"github.com/klauspost/fox/internal/optimize"
	"github.com/ncruces/zenity"
)

func TestMain(m *testing.M) {
	noUI := func(context.Context) ([]string, error) { panic("test opened a native dialog") }
	selectFiles, selectFolder = noUI, noUI
	browse = func(string) error { panic("test opened a browser") }
	os.Exit(m.Run())
}

type fakeEngine struct {
	mu       sync.Mutex
	jobs     []optimize.Event
	lastID   int64
	subs     map[int]func(optimize.Event)
	nsub     int
	settings *config.Settings
}

func newFake() *fakeEngine {
	return &fakeEngine{subs: map[int]func(optimize.Event){}, settings: config.Default()}
}

func (f *fakeEngine) Add(path string, upload bool) []int64 {
	st, err := os.Stat(path)
	var size int64
	if err == nil {
		size = st.Size()
	}
	f.mu.Lock()
	f.lastID++
	e := optimize.Event{ID: f.lastID, Path: path, State: optimize.Queued, Original: size, Size: size, Upload: upload}
	f.jobs = append(f.jobs, e)
	f.mu.Unlock()
	f.emit(e)
	return []int64{e.ID}
}

// set replaces a job's state and notifies subscribers, as the engine would.
func (f *fakeEngine) set(e optimize.Event) {
	f.mu.Lock()
	for i := range f.jobs {
		if f.jobs[i].ID == e.ID {
			f.jobs[i] = e
		}
	}
	f.mu.Unlock()
	f.emit(e)
}

func (f *fakeEngine) emit(e optimize.Event) {
	f.mu.Lock()
	var fns []func(optimize.Event)
	for _, fn := range f.subs {
		fns = append(fns, fn)
	}
	f.mu.Unlock()
	for _, fn := range fns {
		fn(e)
	}
}

func (f *fakeEngine) Subscribe(fn func(optimize.Event)) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nsub++
	n := f.nsub
	f.subs[n] = fn
	return func() {
		f.mu.Lock()
		delete(f.subs, n)
		f.mu.Unlock()
	}
}

func (f *fakeEngine) Jobs() []optimize.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]optimize.Event(nil), f.jobs...)
}

func (f *fakeEngine) Job(id int64) (optimize.Event, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, j := range f.jobs {
		if j.ID == id {
			return j, true
		}
	}
	return optimize.Event{}, false
}

func (f *fakeEngine) Cancel(int64) {}

func (f *fakeEngine) Remove(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keep []optimize.Event
	for _, j := range f.jobs {
		finished := j.State == optimize.Done || j.State == optimize.Skipped || j.State == optimize.Failed
		if !finished || (id != 0 && j.ID != id) {
			keep = append(keep, j)
		}
	}
	f.jobs = keep
}

func (f *fakeEngine) Settings() *config.Settings {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings.Clone()
}

func (f *fakeEngine) SetSettings(s *config.Settings) {
	f.mu.Lock()
	f.settings = s
	f.mu.Unlock()
}

type harness struct {
	t    *testing.T
	eng  *fakeEngine
	srv  *server
	ts   *httptest.Server
	base string
}

func start(t *testing.T) *harness {
	t.Helper()
	eng := newFake()
	s, err := newServer(eng, func() []toolInfo { return []toolInfo{{Name: "oxipng", Available: true}} })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(s.uploadDir) })
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)
	go s.evictLoop(t.Context())
	u, _ := url.Parse(ts.URL)
	s.hosts = []string{u.Host, "localhost:" + u.Port()}
	return &harness{t: t, eng: eng, srv: s, ts: ts, base: ts.URL + "/" + s.token + "/"}
}

func (h *harness) do(method, path, ctype string, body io.Reader) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(method, h.base+path, body)
	if err != nil {
		h.t.Fatal(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (h *harness) json(method, path string, in, out any) int {
	h.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	resp := h.do(method, path, "application/json", body)
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			h.t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func TestAccessControl(t *testing.T) {
	h := start(t)
	get := func(target string, mod func(*http.Request)) int {
		req, _ := http.NewRequest("GET", target, nil)
		if mod != nil {
			mod(req)
		}
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	tok := h.srv.token
	for _, tc := range []struct {
		name   string
		target string
		mod    func(*http.Request)
		want   int
	}{
		{"index", h.base, nil, http.StatusOK},
		{"asset", h.base + "app.js", nil, http.StatusOK},
		{"localhost", h.base, func(r *http.Request) { r.Host = "localhost:" + strings.Split(r.URL.Host, ":")[1] }, http.StatusOK},
		{"no slash", h.ts.URL + "/" + tok, nil, http.StatusFound},
		{"rebinding", h.base, func(r *http.Request) { r.Host = "evil.example:80" }, http.StatusForbidden},
		{"wrong port", h.base, func(r *http.Request) { r.Host = "127.0.0.1:1" }, http.StatusForbidden},
		{"foreign origin", h.base + "api/settings", func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }, http.StatusForbidden},
		{"no token", h.ts.URL + "/", nil, http.StatusNotFound},
		{"wrong token", h.ts.URL + "/" + strings.Repeat("A", len(tok)) + "/", nil, http.StatusNotFound},
		{"token prefix", h.ts.URL + "/" + tok[:10] + "/", nil, http.StatusNotFound},
		{"api without token", h.ts.URL + "/api/settings", nil, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := get(tc.target, tc.mod); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}

	resp := h.do("GET", "", "", nil)
	if p := resp.Header.Get("Referrer-Policy"); p != "no-referrer" {
		t.Errorf("Referrer-Policy = %q", p)
	}
	if b, _ := io.ReadAll(resp.Body); !bytes.Contains(b, []byte("Donate to the original author")) {
		t.Error("index lacks donate link")
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	h := start(t)

	var s config.Settings
	if code := h.json("GET", "api/settings", nil, &s); code != http.StatusOK || s.Level != 5 {
		t.Fatalf("get: %d level %d", code, s.Level)
	}
	var got config.Settings
	code := h.json("PUT", "api/settings", map[string]any{"level": 42, "jpeg": map[string]any{"lossy": true}, "include": []string{"*.png"}}, &got)
	if code != http.StatusOK {
		t.Fatalf("put: %d", code)
	}
	if got.Level != 9 || !got.JPEG.Lossy || got.Include[0] != "*.png" || !got.PreserveTimestamps {
		t.Errorf("put returned %+v", got)
	}
	if e := h.eng.Settings(); e.Level != 9 || !e.JPEG.Lossy {
		t.Errorf("engine settings not applied: %+v", e)
	}
	b, err := os.ReadFile(filepath.Join(home, ".fox", "config.json"))
	if err != nil || !bytes.Contains(b, []byte(`"level": 9`)) {
		t.Errorf("config not saved: %v %s", err, b)
	}
	if code := h.json("PUT", "api/settings", nil, nil); code != http.StatusBadRequest {
		t.Errorf("empty body: %d", code)
	}
	big := `{"include":["` + strings.Repeat("x", maxJSON) + `"]}`
	if resp := h.do("PUT", "api/settings", "application/json", strings.NewReader(big)); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("oversized body: %d", resp.StatusCode)
	}
}

func uploadBody(t *testing.T, name string, data []byte) (string, io.Reader) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	w, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(data)
	mw.Close()
	return mw.FormDataContentType(), &buf
}

func TestUploadDownload(t *testing.T) {
	h := start(t)
	ct, body := uploadBody(t, `C:\Users\x\..\secret\pic.png`, []byte("original-content"))
	resp := h.do("POST", "api/upload", ct, body)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload: %d %s", resp.StatusCode, b)
	}
	var res struct{ ID int64 }
	json.NewDecoder(resp.Body).Decode(&res)
	job, ok := h.eng.Job(res.ID)
	if !ok || !job.Upload || filepath.Base(job.Path) != "pic.png" || job.Original != int64(len("original-content")) {
		t.Fatalf("job %+v", job)
	}
	if rel, err := filepath.Rel(h.srv.uploadDir, job.Path); err != nil || !filepath.IsLocal(rel) {
		t.Fatalf("upload escaped temp dir: %s", job.Path)
	}

	if r := h.do("GET", "api/download/1", "", nil); r.StatusCode != http.StatusConflict {
		t.Errorf("download before done: %d", r.StatusCode)
	}
	os.WriteFile(job.Path, []byte("small"), 0o600)
	job.State, job.Size = optimize.Done, 5
	h.eng.set(job)

	r := h.do("GET", "api/download/1", "", nil)
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK || string(b) != "small" {
		t.Fatalf("download: %d %q", r.StatusCode, b)
	}
	if cd := r.Header.Get("Content-Disposition"); cd != `attachment; filename=pic.png` {
		t.Errorf("Content-Disposition = %q", cd)
	}

	// Jobs added by path must never be downloadable.
	other := filepath.Join(t.TempDir(), "local.png")
	os.WriteFile(other, []byte("x"), 0o600)
	h.eng.Add(other, false)
	h.eng.set(optimize.Event{ID: 2, Path: other, State: optimize.Done})
	if r := h.do("GET", "api/download/2", "", nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("download of local job: %d", r.StatusCode)
	}

	if code := h.json("POST", "api/clear", nil, nil); code != http.StatusNoContent {
		t.Fatalf("clear: %d", code)
	}
	if _, err := os.Stat(filepath.Dir(job.Path)); !os.IsNotExist(err) {
		t.Errorf("upload dir not removed: %v", err)
	}
	if r := h.do("GET", "api/download/1", "", nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("download after clear: %d", r.StatusCode)
	}
}

func TestUploadRejects(t *testing.T) {
	h := start(t)
	for _, name := range []string{"", ".", "..", "/", `..\`, "a\x00b.png"} {
		ct, body := uploadBody(t, name, []byte("x"))
		if r := h.do("POST", "api/upload", ct, body); r.StatusCode != http.StatusBadRequest {
			t.Errorf("name %q: %d", name, r.StatusCode)
		}
	}
	if r := h.do("POST", "api/upload", "text/plain", strings.NewReader("x")); r.StatusCode != http.StatusBadRequest {
		t.Errorf("non-multipart: %d", r.StatusCode)
	}
	if n := len(h.eng.Jobs()); n != 0 {
		t.Errorf("%d jobs queued from rejected uploads", n)
	}
}

func TestUploadName(t *testing.T) {
	for in, want := range map[string]string{
		"a.png":                  "a.png",
		"dir/b.jpg":              "b.jpg",
		`C:\x\y\c.gif`:           "c.gif",
		"../../etc/passwd":       "passwd",
		"ünïcödé ✓.svg":          "ünïcödé ✓.svg",
		"":                       "",
		"..":                     "",
		"x/..":                   "",
		"a\nb.png":               "",
		strings.Repeat("a", 300): "",
	} {
		got, ok := uploadName(in)
		if got != want || ok != (want != "") {
			t.Errorf("uploadName(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestAddPaths(t *testing.T) {
	h := start(t)
	var res struct{ IDs []int64 }
	if code := h.json("POST", "api/add", map[string]any{"paths": []string{` "C:\a b\c.png" `, "", "  "}}, &res); code != http.StatusOK {
		t.Fatal(code)
	}
	if len(res.IDs) != 1 {
		t.Fatalf("ids %v", res.IDs)
	}
	if j, _ := h.eng.Job(res.IDs[0]); j.Path != `C:\a b\c.png` || j.Upload {
		t.Errorf("job %+v", j)
	}
}

type sse struct {
	r *bufio.Reader
}

func (s sse) next(t *testing.T) (string, string) {
	t.Helper()
	var event, data string
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "" && event != "":
			return event, data
		case strings.HasPrefix(line, "event: "):
			event = line[7:]
		case strings.HasPrefix(line, "data: "):
			data = line[6:]
		}
	}
}

func (h *harness) events() sse {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	h.t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", h.base+"api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { resp.Body.Close() })
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		h.t.Fatalf("content type %q", ct)
	}
	return sse{bufio.NewReader(resp.Body)}
}

func TestEvents(t *testing.T) {
	h := start(t)
	h.eng.Add("/x/one.png", false)
	s := h.events()

	ev, data := s.next(t)
	var snap []optimize.Event
	if ev != "snapshot" || json.Unmarshal([]byte(data), &snap) != nil || len(snap) != 1 || snap[0].Path != "/x/one.png" {
		t.Fatalf("first event %s %s", ev, data)
	}
	if ev, _ := s.next(t); ev != "stats" {
		t.Fatalf("expected stats, got %s", ev)
	}

	// Several updates to one job between flushes collapse into the latest.
	for _, st := range []optimize.State{optimize.Running, optimize.Running, optimize.Done} {
		h.eng.set(optimize.Event{ID: 1, Path: "/x/one.png", State: st, Original: 100, Size: 60})
	}
	var last optimize.Event
	var stats stats
	for stats.Done == 0 {
		switch ev, data := s.next(t); ev {
		case "job":
			json.Unmarshal([]byte(data), &last)
		case "stats":
			json.Unmarshal([]byte(data), &stats)
		}
	}
	if last.State != optimize.Done || stats.Original != 100 || stats.Size != 60 || stats.Queued+stats.Running != 0 {
		t.Errorf("last %+v stats %+v", last, stats)
	}

	// A change to a job the engine no longer has (removed before the flush) must not resurrect it.
	h.eng.emit(optimize.Event{ID: 99, State: optimize.Done})
	h.eng.set(optimize.Event{ID: 1, Path: "/x/one.png", State: optimize.Done, Original: 100, Size: 50})
	for last.Size != 50 {
		switch ev, data := s.next(t); ev {
		case "job":
			json.Unmarshal([]byte(data), &last)
			if last.ID != 1 {
				t.Fatalf("event for unknown job: %s", data)
			}
		case "snapshot":
			t.Fatalf("unexpected snapshot %s", data)
		}
	}

	// Removing jobs resends the snapshot, so the browser drops them; the totals stay,
	// and the second final event of job 1 was not counted again.
	h.json("POST", "api/clear", nil, nil)
	for {
		ev, data := s.next(t)
		if ev == "snapshot" {
			if data != "[]" {
				t.Errorf("snapshot after clear: %s", data)
			}
			break
		}
	}
	if ev, data := s.next(t); ev != "stats" || json.Unmarshal([]byte(data), &stats) != nil || stats.Done != 1 || stats.Size != 60 {
		t.Errorf("stats after clear: %s %s", ev, data)
	}

	if code := h.json("POST", "api/stats/reset", nil, nil); code != http.StatusNoContent {
		t.Fatalf("reset: %d", code)
	}
	if ev, data := s.next(t); ev != "stats" || data != `{"queued":0,"running":0,"done":0,"skipped":0,"failed":0,"original":0,"size":0,"elapsed":0}` {
		t.Errorf("stats after reset: %s %s", ev, data)
	}
}

func TestSessionTotals(t *testing.T) {
	h := start(t)
	for _, p := range []string{"/a.png", "/b.png", "/c.png", "/d.png"} {
		h.eng.Add(p, false)
	}
	h.eng.set(optimize.Event{ID: 1, State: optimize.Running, Original: 100, Size: 80})
	h.eng.set(optimize.Event{ID: 1, State: optimize.Done, Original: 100, Size: 60})
	h.eng.set(optimize.Event{ID: 1, State: optimize.Done, Original: 100, Size: 60})
	h.eng.set(optimize.Event{ID: 2, State: optimize.Skipped, Original: 50, Size: 50})
	h.eng.set(optimize.Event{ID: 3, State: optimize.Failed})
	h.eng.emit(optimize.Event{ID: 99, State: optimize.Done, Original: 1000})
	h.eng.set(optimize.Event{ID: 4, State: optimize.Running})
	time.Sleep(20 * time.Millisecond) // Windows' monotonic clock is coarse

	check := func(when string, want stats) time.Duration {
		t.Helper()
		got := h.srv.stats(h.eng.Jobs())
		el := got.Elapsed
		got.Elapsed = 0
		if got != want {
			t.Errorf("%s: got %+v, want %+v", when, got, want)
		}
		return el
	}
	if el := check("running", stats{Running: 1, Done: 1, Skipped: 1, Failed: 1, Original: 100, Size: 60}); el < 20*time.Millisecond {
		t.Errorf("elapsed while busy %v", el)
	}
	h.json("POST", "api/remove", map[string]int64{"id": 1}, nil)
	h.json("POST", "api/clear", nil, nil)
	if n := len(h.eng.Jobs()); n != 1 {
		t.Fatalf("%d jobs after clear", n)
	}
	check("cleared", stats{Running: 1, Done: 1, Skipped: 1, Failed: 1, Original: 100, Size: 60})

	h.json("POST", "api/stats/reset", nil, nil)
	if el := check("reset", stats{Running: 1}); el >= 20*time.Millisecond {
		t.Errorf("elapsed not reset: %v", el)
	}
	time.Sleep(20 * time.Millisecond)
	// A job running during the reset counts once it finishes.
	h.eng.set(optimize.Event{ID: 4, State: optimize.Done, Original: 10, Size: 5})
	el := check("after reset", stats{Done: 1, Original: 10, Size: 5})
	if el < 20*time.Millisecond || el > time.Minute {
		t.Errorf("elapsed after reset %v", el)
	}
	time.Sleep(20 * time.Millisecond)
	if el2 := h.srv.stats(nil).Elapsed; el2 != el {
		t.Errorf("elapsed grew while idle: %v -> %v", el, el2)
	}
}

func TestEvictFinished(t *testing.T) {
	defer func(n int) { maxFinished = n }(maxFinished)
	maxFinished = 2
	h := start(t)
	s := h.events()

	upload := func(name string) optimize.Event {
		ct, body := uploadBody(t, name, []byte("original-content"))
		var res struct{ ID int64 }
		json.NewDecoder(h.do("POST", "api/upload", ct, body).Body).Decode(&res)
		j, _ := h.eng.Job(res.ID)
		os.WriteFile(j.Path, []byte("small"), 0o600)
		j.State, j.Size = optimize.Done, 5
		h.eng.set(j)
		return j
	}
	pending := upload("keep.png") // 1: never downloaded, must stay
	got := upload("got.png")      // 2: downloaded, may go
	// Neither delivers the whole result.
	h.do("HEAD", "api/download/1", "", nil).Body.Close()
	req, _ := http.NewRequest("GET", h.base+"api/download/1", nil)
	req.Header.Set("Range", "bytes=0-1")
	if r, err := http.DefaultClient.Do(req); err != nil || r.StatusCode != http.StatusPartialContent {
		t.Fatalf("range download: %v %v", r, err)
	} else {
		io.ReadAll(r.Body)
		r.Body.Close()
	}
	if r := h.do("GET", "api/download/2", "", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("download: %d", r.StatusCode)
	} else {
		io.ReadAll(r.Body)
	}
	h.eng.Add("/queued.png", false) // 3: active, never evicted
	for i, st := range []optimize.State{optimize.Done, optimize.Skipped, optimize.Failed} {
		id := h.eng.Add(fmt.Sprintf("/local%d.png", i), false)[0]
		h.eng.set(optimize.Event{ID: id, State: st})
	}

	removed := map[int64]bool{}
	for len(removed) < 3 {
		ev, data := s.next(t)
		if ev != "remove" {
			continue
		}
		var ids []int64
		json.Unmarshal([]byte(data), &ids)
		for _, id := range ids {
			removed[id] = true
		}
	}
	if !removed[2] || !removed[4] || !removed[5] {
		t.Errorf("removed %v, want 2, 4 and 5", removed)
	}
	var left []int64
	for _, j := range h.eng.Jobs() {
		left = append(left, j.ID)
	}
	if fmt.Sprint(left) != "[1 3 6]" {
		t.Errorf("jobs left %v, want [1 3 6]", left)
	}
	if _, err := os.Stat(got.Path); !os.IsNotExist(err) {
		t.Errorf("evicted upload not deleted: %v", err)
	}
	if _, err := os.Stat(pending.Path); err != nil {
		t.Errorf("pending upload deleted: %v", err)
	}
	if st := h.srv.stats(nil); st.Done != 3 || st.Skipped != 1 || st.Failed != 1 {
		t.Errorf("totals after eviction %+v", st)
	}

	// A clear recounts, so jobs finishing later do not each trigger a scan.
	h.json("POST", "api/clear", nil, nil)
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		h.srv.sess.mu.Lock()
		n := h.srv.sess.finished
		h.srv.sess.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("finished estimate %d after clear", n)
		}
	}
}

func TestWaitIdle(t *testing.T) {
	h := start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { h.srv.waitIdle(ctx, 50*time.Millisecond, 5*time.Millisecond); close(done) }()

	select {
	case <-done:
		t.Fatal("exited before any client connected")
	case <-time.After(150 * time.Millisecond):
	}

	c := newClient()
	h.srv.addClient(c)
	h.eng.Add("/busy.png", false)
	h.srv.dropClient(c)
	select {
	case <-done:
		t.Fatal("exited with queued jobs")
	case <-time.After(150 * time.Millisecond):
	}

	h.eng.set(optimize.Event{ID: 1, State: optimize.Done})
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("did not exit when idle")
	}
	if ctx.Err() != nil {
		t.Fatal("timed out")
	}
}

func TestPick(t *testing.T) {
	h := start(t)
	defer func(f, d func(context.Context) ([]string, error)) { selectFiles, selectFolder = f, d }(selectFiles, selectFolder)
	selectFiles = func(context.Context) ([]string, error) { return []string{"/a.png", "/it's'"}, nil }
	selectFolder = func(context.Context) ([]string, error) { return nil, zenity.ErrCanceled }

	var res struct{ IDs []int64 }
	if code := h.json("POST", "api/pick", map[string]bool{"folder": false}, &res); code != http.StatusOK || len(res.IDs) != 2 {
		t.Fatalf("files: %d %v", code, res.IDs)
	}
	// Paths from the dialog are exact; only typed paths get quotes trimmed.
	if j, _ := h.eng.Job(res.IDs[1]); j.Path != "/it's'" {
		t.Errorf("dialog path altered: %q", j.Path)
	}
	res.IDs = nil
	if code := h.json("POST", "api/pick", map[string]bool{"folder": true}, &res); code != http.StatusOK || res.IDs == nil || len(res.IDs) != 0 {
		t.Fatalf("cancelled folder: %d %v", code, res.IDs)
	}
}

// TestRunShutdown checks that cancelling Run also ends requests blocked in a file dialog.
func TestRunShutdown(t *testing.T) {
	defer func(f func(context.Context) ([]string, error)) { selectFiles = f }(selectFiles)
	inDialog := make(chan struct{})
	selectFiles = func(ctx context.Context) ([]string, error) {
		close(inDialog)
		<-ctx.Done()
		return nil, zenity.ErrCanceled
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = pw
	defer func() { os.Stdout = stdout }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, newFake(), false) }()

	line, err := bufio.NewReader(pr).ReadString('\n')
	os.Stdout = stdout
	if err != nil {
		t.Fatal(err)
	}
	u := strings.Fields(line)[len(strings.Fields(line))-1]
	if !strings.HasPrefix(u, "http://127.0.0.1:") {
		t.Fatalf("no URL in %q", line)
	}
	go http.Post(u+"api/pick", "application/json", strings.NewReader(`{}`))
	select {
	case <-inDialog:
	case <-time.After(5 * time.Second):
		t.Fatal("pick did not reach the dialog")
	}

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("shutdown waited %v for the open dialog", d)
	}
}

// TestLaunch checks that the browser is given a private redirect file instead of the URL holding the token.
func TestLaunch(t *testing.T) {
	defer func(f func(string) error, d time.Duration, r bool) { browse, launchTTL, redirectFile = f, d, r }(browse, launchTTL, redirectFile)
	const target = "http://127.0.0.1:1234/SECRETTOKEN/"
	var arg string
	browse = func(u string) error { arg = u; return nil }

	redirectFile = false
	if cleanup, err := launch(target); err != nil || arg != target {
		t.Fatalf("direct launch: %q, %v", arg, err)
	} else {
		cleanup()
	}

	redirectFile = true
	launchTTL = time.Hour
	cleanup, err := launch(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(arg, "SECRETTOKEN") || !filepath.IsAbs(arg) {
		t.Fatalf("browser got %q, want the path of a redirect file", arg)
	}
	p := arg
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`content="0;url=` + target + `"`, `location.replace("` + target + `")`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("redirect page lacks %s:\n%s", want, b)
		}
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(p)
		di, _ := os.Stat(filepath.Dir(p))
		if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
			t.Errorf("modes %v %v, want private", fi.Mode(), di.Mode())
		}
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
		t.Errorf("cleanup left %s: %v", p, err)
	}

	launchTTL = 10 * time.Millisecond
	if _, err := launch(target); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, err := os.Stat(filepath.Dir(arg)); !os.IsNotExist(err); _, err = os.Stat(filepath.Dir(arg)) {
		if time.Now().After(deadline) {
			t.Fatalf("%s not removed after launchTTL", arg)
		}
		time.Sleep(10 * time.Millisecond)
	}

	browse = func(u string) error { arg = u; return os.ErrPermission }
	if _, err := launch(target); err == nil {
		t.Fatal("browse error not returned")
	}
	if _, err := os.Stat(filepath.Dir(arg)); !os.IsNotExist(err) {
		t.Errorf("failed launch left its file: %v", err)
	}
}
