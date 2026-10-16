package optimize

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/fox/internal/config"
	"github.com/klauspost/fox/internal/detect"
	"github.com/klauspost/fox/internal/pipeline"
	"github.com/klauspost/fox/internal/tools"
)

// The test binary doubles as a fake optimizer when FOX_FAKE_TOOL is set.
func TestMain(m *testing.M) {
	if os.Getenv("FOX_FAKE_TOOL") != "" {
		os.Exit(fakeTool(os.Args[1], os.Args[2:]))
	}
	os.Setenv("FOX_FAKE_TOOL", "1")
	// Race-enabled children otherwise sleep 1s on exit.
	os.Setenv("GORACE", "atexit_sleep_ms=0")
	os.Exit(m.Run())
}

var fakeModes = []string{"shrink", "smaller", "larger", "tiny", "fail", "sleep", "half", "barrier", "func", "spawn"}

func fakeTool(mode string, args []string) int {
	// Accept placeholders embedded in flags such as "--in={in}".
	arg := func(i int) string {
		if _, v, ok := strings.Cut(args[i], "="); ok {
			return v
		}
		return args[i]
	}
	read := func(p string) []byte {
		b, err := os.ReadFile(p)
		if err != nil {
			panic(err)
		}
		return b
	}
	write := func(p string, b []byte) {
		if err := os.WriteFile(p, b, 0o644); err != nil {
			panic(err)
		}
	}
	switch mode {
	case "shrink":
		b := read(arg(0))
		write(arg(0), b[:len(b)/2])
	case "smaller":
		b := read(arg(0))
		write(arg(1), b[:len(b)/2])
	case "larger":
		b := read(arg(0))
		write(arg(1), append(b, b...))
	case "tiny":
		write(arg(1), []byte("abc"))
	case "fail":
		b := read(arg(0))
		write(arg(0), b[:len(b)/4])
		fmt.Fprintln(os.Stderr, "boom")
		return 1
	case "sleep":
		write(arg(0), nil)
		time.Sleep(time.Minute)
	case "spawn":
		// Starts a grandchild that shares our stderr, then records its pid.
		c := exec.Command(os.Args[0], "sleep", args[1])
		c.Stderr = os.Stderr
		if err := c.Start(); err != nil {
			panic(err)
		}
		write(args[0], []byte(strconv.Itoa(c.Process.Pid)))
		time.Sleep(time.Minute)
	case "half":
		b, _ := io.ReadAll(os.Stdin)
		os.Stdout.Write(b[:len(b)/2])
	case "barrier":
		// Succeeds only if n processes are alive at the same time.
		dir := args[0]
		n, _ := strconv.Atoi(args[1])
		write(filepath.Join(dir, strconv.Itoa(os.Getpid())), nil)
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if es, _ := os.ReadDir(dir); len(es) >= n {
				break
			}
			if time.Now().After(deadline) {
				return 1
			}
		}
		b := read(args[2])
		write(args[2], b[:len(b)/2])
	default:
		return 2
	}
	return 0
}

type fakeResolver struct {
	mu sync.Mutex
	m  map[string][]string
}

func (f *fakeResolver) SetOverrides(m map[string][]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m = m
}

func (f *fakeResolver) Command(name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.m[name]; ok {
		return c, nil
	}
	return nil, tools.ErrNotFound
}

func (f *fakeResolver) Available(name string) bool {
	_, err := f.Command(name)
	return err == nil
}

func newTestEngine(t *testing.T, steps []pipeline.Step, mod func(*config.Settings)) *Engine {
	t.Helper()
	s := config.Default()
	s.Workers = 2
	s.SkipOptimized = false
	s.ToolPaths = map[string][]string{}
	for _, m := range fakeModes {
		s.ToolPaths[m] = []string{os.Args[0], m}
	}
	if mod != nil {
		mod(s)
	}
	dir := t.TempDir()
	e := newEngine(&fakeResolver{}, s, hooks{
		detect: func(p string, _ []string) (detect.Format, error) {
			if strings.EqualFold(filepath.Ext(p), ".png") {
				return detect.PNG, nil
			}
			return "", nil
		},
		steps: func(detect.Format, string, *config.Settings) ([]pipeline.Step, string) {
			return slices.Clone(steps), ""
		},
		cache: filepath.Join(dir, "cache"),
		tmp:   filepath.Join(dir, "tmp"),
	})
	t.Cleanup(e.Close)
	return e
}

func step(tool string, args ...string) pipeline.Step { return pipeline.Step{Tool: tool, Args: args} }

func testFile(t *testing.T, dir, name string, size int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, bytes.Repeat([]byte("0123456789abcdef"), size/16), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func runOne(t *testing.T, e *Engine, path string) Event {
	t.Helper()
	ids := e.Add(path, false)
	if len(ids) != 1 {
		t.Fatalf("Add returned %v", ids)
	}
	e.Wait()
	ev, _ := e.Job(ids[0])
	return ev
}

func fileSize(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func TestSteps(t *testing.T) {
	smallerFunc := pipeline.Step{Tool: "func", Func: func(_ context.Context, x *pipeline.Exec, in, out string) error {
		if filepath.Dir(in) != x.Dir {
			return fmt.Errorf("in %q not in %q", in, x.Dir)
		}
		b, err := os.ReadFile(in)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b[:len(b)/4], 0o644)
	}}
	reject := step("smaller", "{in}", "{out}")
	reject.Accept = func(cur, cand int64) bool { return false }
	tests := []struct {
		name  string
		steps []pipeline.Step
		want  int64
	}{
		{"in-place", []pipeline.Step{step("shrink", "{in}")}, 512},
		{"out", []pipeline.Step{step("smaller", "{in}", "{out}")}, 512},
		{"embedded placeholders", []pipeline.Step{step("smaller", "--in={in}", "-out={out}")}, 512},
		{"chained", []pipeline.Step{step("shrink", "{in}"), step("smaller", "{in}", "{out}")}, 256},
		{"stdio", []pipeline.Step{{Tool: "half", Stdio: true}}, 512},
		{"func", []pipeline.Step{smallerFunc}, 256},
		{"larger rejected", []pipeline.Step{step("larger", "{in}", "{out}")}, 1024},
		{"tiny rejected", []pipeline.Step{step("tiny", "{in}", "{out}")}, 1024},
		{"accept rejects", []pipeline.Step{reject}, 1024},
		{"failure ignored", []pipeline.Step{step("fail", "{in}"), step("smaller", "{in}", "{out}")}, 512},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(t, tc.steps, nil)
			p := testFile(t, t.TempDir(), "a.png", 1024)
			ev := runOne(t, e, p)
			if ev.State != Done || ev.Original != 1024 || ev.Size != tc.want {
				t.Fatalf("got %+v, want done with size %d", ev, tc.want)
			}
			if got := fileSize(t, p); got != tc.want {
				t.Fatalf("file size %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAllStepsFail(t *testing.T) {
	e := newTestEngine(t, []pipeline.Step{step("fail", "{in}")}, func(s *config.Settings) { s.SkipOptimized = true })
	p := testFile(t, t.TempDir(), "a.png", 1024)
	for range 2 {
		// The second run must retry rather than be skipped from the cache.
		ev := runOne(t, e, p)
		if ev.State != Done || ev.Size != 1024 || !strings.Contains(ev.Message, "boom") {
			t.Fatalf("got %+v", ev)
		}
	}
	if fileSize(t, p) != 1024 {
		t.Fatal("file modified")
	}
}

func TestWalk(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var want []string
	for _, name := range []string{
		"a.png", "sub/b.PNG", "sub/x.mjs", "node_modules/c.png", "dir.git/d.png",
		"-.git/lfs/objects/ab/cd/e.png", "-.git/lfs/objects/ab/cd/0123abcd", "-sub/.SVN/pristine/f.png",
		"-node_modules/.cache/g.png", "-.cache/node_modules/_cacache/h.png", "-fox-tools/i.png",
		"-__pycache__/j.png", "-.yarn/cache/l.png", "-node_modules/.pnpm/m/n.png", "-noext", "-k.txt",
	} {
		skip := strings.HasPrefix(name, "-")
		p := filepath.Join(dir, strings.TrimPrefix(name, "-"))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		if !skip {
			want = append(want, p)
		}
	}
	e := newTestEngine(t, []pipeline.Step{step("shrink", "{in}")}, func(s *config.Settings) { s.Web.JSExtensions = []string{"mjs"} })
	e.h.detect = detect.Detect
	e.Add(dir, false)
	e.Wait()
	var got []string
	for _, ev := range e.Jobs() {
		got = append(got, ev.Path)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("queued %q\nwant %q", got, want)
	}

	// The CLI expands "dir/*" into explicit paths, so a skipped dir stays skipped when named.
	for _, p := range []string{filepath.Join(dir, ".git"), filepath.Join(dir, "fox-tools")} {
		if ids := e.Add(p, false); len(ids) != 0 {
			t.Errorf("%s: queued %v", p, ids)
		}
	}
	// Named explicitly, an extensionless file is sniffed and a dir below a skipped one is walked.
	for _, p := range []string{filepath.Join(dir, "noext"), filepath.Join(dir, ".git", "lfs")} {
		ids := e.Add(p, false)
		e.Wait()
		if len(ids) != 1 {
			t.Fatalf("%s: queued %v", p, ids)
		}
		if ev, _ := e.Job(ids[0]); ev.State != Done || ev.Format != detect.PNG || ev.Size >= ev.Original {
			t.Errorf("%s: got %+v", p, ev)
		}
	}
}

func TestEventsAndFiltering(t *testing.T) {
	for _, tc := range []struct {
		disabled []string
		want     []string
	}{
		{nil, []string{"shrink (1/2)", "smaller (2/2)"}},
		{[]string{"shrink"}, []string{"smaller (1/1)"}},
	} {
		e := newTestEngine(t, []pipeline.Step{step("shrink", "{in}"), step("missing", "{in}"), step("smaller", "{in}", "{out}")},
			func(s *config.Settings) { s.DisabledTools = tc.disabled })
		var mu sync.Mutex
		var states []State
		var names []string
		defer e.Subscribe(func(ev Event) {
			mu.Lock()
			defer mu.Unlock()
			states = append(states, ev.State)
			if ev.Step != "" {
				names = append(names, ev.Step)
			}
		})()
		runOne(t, e, testFile(t, t.TempDir(), "a.png", 1024))
		mu.Lock()
		if !slices.Equal(names, tc.want) {
			t.Errorf("steps %q, want %q", names, tc.want)
		}
		if states[0] != Queued || states[len(states)-1] != Done {
			t.Errorf("states %v", states)
		}
		mu.Unlock()
	}
}

func TestSkips(t *testing.T) {
	dir := t.TempDir()
	e := newTestEngine(t, []pipeline.Step{step("shrink", "{in}")}, func(s *config.Settings) { s.Exclude = []string{"X*.PNG"} })
	ro := testFile(t, dir, "ro.png", 64)
	if err := os.Chmod(ro, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o644) })
	for name, want := range map[string]string{
		ro:                                       "read-only",
		testFile(t, dir, "a.txt", 64):            "unsupported format",
		testFile(t, dir, "xcluded.png", 64):      "excluded by filter",
		filepath.Join(dir, "missing", "nothing"): "",
	} {
		ev := runOne(t, e, name)
		if want == "" {
			if ev.State != Failed {
				t.Errorf("%s: got %+v, want failed", name, ev)
			}
			continue
		}
		if ev.State != Skipped || ev.Message != want {
			t.Errorf("%s: got %+v, want skipped %q", name, ev, want)
		}
	}
	if fileSize(t, ro) != 64 {
		t.Fatal("read-only file modified")
	}

	e = newTestEngine(t, []pipeline.Step{step("missing", "{in}")}, nil)
	if ev := runOne(t, e, testFile(t, dir, "b.png", 64)); ev.State != Skipped || ev.Message != "no tools available" {
		t.Errorf("got %+v", ev)
	}
}

func waitFor(t *testing.T, e *Engine, id int64, ok func(Event) bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ev, _ := e.Job(id); ok(ev) {
			return
		}
	}
	ev, _ := e.Job(id)
	t.Fatalf("timeout, job %+v", ev)
}

func TestCancel(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(t.TempDir(), "started")
	e := newTestEngine(t, []pipeline.Step{step("sleep", started, "{in}")}, func(s *config.Settings) { s.Workers = 1 })
	ids := e.Add(dir, false) // empty dir
	if len(ids) != 0 {
		t.Fatal(ids)
	}
	a, b := testFile(t, dir, "a.png", 64), testFile(t, dir, "b.png", 64)
	ida, idb := e.Add(a, false)[0], e.Add(b, false)[0]
	waitFor(t, e, ida, func(Event) bool { _, err := os.Stat(started); return err == nil })

	e.Cancel(idb)
	if ev, _ := e.Job(idb); ev.State != Skipped || ev.Message != "cancelled" {
		t.Fatalf("queued job: %+v", ev)
	}
	start := time.Now()
	e.Cancel(ida)
	e.Wait()
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("cancel took %v, tool was not killed", d)
	}
	if ev, _ := e.Job(ida); ev.State != Skipped || ev.Message != "cancelled" {
		t.Fatalf("running job: %+v", ev)
	}
	if fileSize(t, a) != 64 {
		t.Fatal("file modified")
	}
}

func TestConcurrency(t *testing.T) {
	const n = 3
	dir, barrier := t.TempDir(), t.TempDir()
	e := newTestEngine(t, []pipeline.Step{step("barrier", barrier, strconv.Itoa(n), "{in}")}, func(s *config.Settings) { s.Workers = n })
	for i := range n {
		testFile(t, dir, fmt.Sprintf("%d.png", i), 64)
	}
	ids := e.Add(dir, false)
	e.Wait()
	if len(ids) != n {
		t.Fatal(ids)
	}
	for _, id := range ids {
		if ev, _ := e.Job(id); ev.State != Done || ev.Size != 32 {
			t.Errorf("got %+v", ev)
		}
	}
}

func TestTimestamps(t *testing.T) {
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, preserve := range []bool{true, false} {
		e := newTestEngine(t, []pipeline.Step{step("shrink", "{in}")}, func(s *config.Settings) { s.PreserveTimestamps = preserve })
		p := testFile(t, t.TempDir(), "a.png", 64)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		if ev := runOne(t, e, p); ev.State != Done || ev.Size != 32 {
			t.Fatalf("got %+v", ev)
		}
		fi, _ := os.Stat(p)
		if got := fi.ModTime().Equal(old); got != preserve {
			t.Errorf("preserve=%v: mtime %v", preserve, fi.ModTime())
		}
	}
}

func TestModifiedDuringOptimization(t *testing.T) {
	p := testFile(t, t.TempDir(), "a.png", 64)
	e := newTestEngine(t, []pipeline.Step{{Tool: "func", Func: func(_ context.Context, _ *pipeline.Exec, in, out string) error {
		if err := os.WriteFile(p, []byte("user edited this file meanwhile"), 0o644); err != nil {
			return err
		}
		return os.WriteFile(out, []byte("optimized!"), 0o644)
	}}}, nil)
	ev := runOne(t, e, p)
	if ev.State != Failed || ev.Message != errModified.Error() {
		t.Fatalf("got %+v", ev)
	}
	if b, _ := os.ReadFile(p); string(b) != "user edited this file meanwhile" {
		t.Fatalf("file overwritten: %q", b)
	}
}

func blockingStep(release <-chan struct{}) pipeline.Step {
	return pipeline.Step{Tool: "func", Func: func(context.Context, *pipeline.Exec, string, string) error {
		<-release
		return nil
	}}
}

func TestDedupeAndRemove(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	e := newTestEngine(t, []pipeline.Step{blockingStep(release)}, func(s *config.Settings) { s.Workers = 1 })
	a := testFile(t, dir, "a.png", 64)
	ida := e.Add(a, false)
	if len(ida) != 1 {
		t.Fatal(ida)
	}
	if ids := e.Add(a, false); len(ids) != 0 {
		t.Fatalf("duplicate queued: %v", ids)
	}
	b := testFile(t, dir, "b.png", 64)
	if ids := e.Add(dir, false); len(ids) != 1 {
		t.Fatalf("dir add: %v", ids)
	} else if ev, _ := e.Job(ids[0]); ev.Path != b {
		t.Fatalf("dir add queued %q", ev.Path)
	}
	waitFor(t, e, ida[0], func(ev Event) bool { return ev.State == Running })
	e.Remove(0)
	if n := len(e.Jobs()); n != 2 {
		t.Fatalf("removed unfinished jobs, %d left", n)
	}
	close(release)
	e.Wait()

	if ids := e.Add(a, false); len(ids) != 1 {
		t.Fatalf("finished file not re-queued: %v", ids)
	}
	e.Wait()
	e.Remove(ida[0])
	if _, ok := e.Job(ida[0]); ok {
		t.Fatal("job not removed")
	}
	e.Remove(0)
	if jobs := e.Jobs(); len(jobs) != 0 {
		t.Fatalf("jobs left: %+v", jobs)
	}
}

func TestCache(t *testing.T) {
	e := newTestEngine(t, []pipeline.Step{step("shrink", "{in}")}, func(s *config.Settings) { s.SkipOptimized = true })
	p := testFile(t, t.TempDir(), "a.png", 64)
	if ev := runOne(t, e, p); ev.State != Done || ev.Size != 32 {
		t.Fatalf("got %+v", ev)
	}
	if ev := runOne(t, e, p); ev.State != Skipped || ev.Message != "already optimized" {
		t.Fatalf("got %+v", ev)
	}
	// The cache is persistent and keyed on the settings fingerprint.
	s := e.Settings()
	e2 := newEngine(&fakeResolver{}, s, e.h)
	t.Cleanup(e2.Close)
	if ev := runOne(t, e2, p); ev.State != Skipped {
		t.Fatalf("reloaded cache: got %+v", ev)
	}
	s.Level++
	e2.SetSettings(s)
	if ev := runOne(t, e2, p); ev.State != Done || ev.Size != 16 {
		t.Fatalf("new settings: got %+v", ev)
	}
}

func TestHelpers(t *testing.T) {
	if got := expand([]string{"{in}", "--in={in}", "-file:{out}", "x"}, "I", "O"); !slices.Equal(got, []string{"I", "--in=I", "-file:O", "x"}) {
		t.Error(got)
	}
	for _, tc := range []struct {
		path string
		f    detect.Format
		want string
	}{
		{"a.PNG", detect.PNG, ".png"},
		{"a.apng", detect.PNG, ".png"},
		{"renamed.jpg", detect.PNG, ".png"},
		{"a", detect.JPEG, ".jpeg"},
		{"a.ü", detect.GIF, ".gif"},
		{"a.tar.gz", detect.GZ, ".gz"},
		{"a.MOV", detect.MP4, ".mov"},
		{"mkv.mp4", detect.MKV, ".mkv"},
		{"a.webm", detect.MKV, ".webm"},
		{"a.dng", detect.TIFF, ".dng"},
	} {
		if got := tmpExt(tc.path, tc.f); got != tc.want {
			t.Errorf("tmpExt(%q, %s) = %q, want %q", tc.path, tc.f, got, tc.want)
		}
	}
}

func TestStaleTempDirs(t *testing.T) {
	e := newTestEngine(t, []pipeline.Step{step("shrink", "{in}")}, nil)
	stale, live := filepath.Join(e.h.tmp, "jstale"), filepath.Join(e.h.tmp, "jlive")
	for _, d := range []string{stale, live} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	runOne(t, e, testFile(t, t.TempDir(), "a.png", 64))
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale job dir kept")
	}
	if _, err := os.Stat(live); err != nil {
		t.Error("recent job dir removed")
	}
}
