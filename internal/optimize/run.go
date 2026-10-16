package optimize

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/klauspost/fox/internal/config"
	"github.com/klauspost/fox/internal/detect"
	"github.com/klauspost/fox/internal/pipeline"
)

// task is one job being processed by a worker.
type task struct {
	e     *Engine
	j     *job
	ev    Event
	s     *config.Settings
	start time.Time
	dir   string // per-job temp dir
	ext   string
}

func (t *task) end(st State, msg string) Event {
	t.ev.State, t.ev.Step, t.ev.Message, t.ev.Elapsed = st, "", msg, time.Since(t.start)
	return t.ev
}

func (t *task) process(ctx context.Context) Event {
	t.start = time.Now()
	path := t.ev.Path
	fi, err := os.Stat(path)
	if err != nil {
		return t.end(Failed, err.Error())
	}
	if !fi.Mode().IsRegular() {
		return t.end(Skipped, "not a regular file")
	}
	t.ev.Original, t.ev.Size = fi.Size(), fi.Size()
	if !selected(t.s, path) {
		return t.end(Skipped, "excluded by filter")
	}
	if fi.Mode().Perm()&0o200 == 0 {
		return t.end(Skipped, "read-only")
	}
	f, err := t.e.h.detect(path, t.s.Web.JSExtensions)
	if err != nil {
		return t.end(Failed, err.Error())
	}
	if f == "" {
		return t.end(Skipped, "unsupported format")
	}
	t.ev.Format, t.ev.State = f, Running
	t.e.publish(t.j, t.ev)

	var sum string
	fp := t.s.Fingerprint()
	if t.s.SkipOptimized {
		if sum, err = hashFile(path); err != nil {
			return t.end(Failed, err.Error())
		}
		if t.e.cache.has(sum + " " + fp) {
			return t.end(Skipped, "already optimized")
		}
	}
	steps, skip := t.e.h.steps(f, path, t.s)
	if skip != "" {
		return t.end(Skipped, skip)
	}
	steps = slices.DeleteFunc(steps, func(st pipeline.Step) bool {
		return st.Tool != "" && (slices.Contains(t.s.DisabledTools, st.Tool) || !t.e.res.Available(st.Tool))
	})
	if len(steps) == 0 {
		return t.end(Skipped, "no tools available")
	}

	base, err := t.e.tmpBase()
	if err != nil {
		return t.end(Failed, err.Error())
	}
	if t.dir, err = os.MkdirTemp(base, "j"); err != nil {
		return t.end(Failed, err.Error())
	}
	defer os.RemoveAll(t.dir)
	t.ext = tmpExt(path, f)
	orig := filepath.Join(t.dir, "orig"+t.ext)
	if err := copyFile(path, orig); err != nil {
		return t.end(Failed, err.Error())
	}
	best, err := t.runSteps(ctx, steps, orig)
	if ctx.Err() != nil {
		return t.end(Skipped, "cancelled")
	}
	// Many tools exit non-zero when they find nothing to improve (shntool on a clean WAV),
	// so a failing pipeline is reported, not treated as an error.
	var note string
	if err != nil {
		note = err.Error()
	}
	if t.ev.Size < t.ev.Original {
		if err := writeBack(path, fi, best, orig, t.s.PreserveTimestamps); err != nil {
			return t.end(Failed, err.Error())
		}
		if t.s.SkipOptimized {
			if sum, err = hashFile(best); err != nil {
				sum = ""
			}
		}
	}
	if sum != "" && note == "" {
		_ = t.e.cache.add(sum + " " + fp)
	}
	return t.end(Done, note)
}

// runSteps returns the smallest version produced, and the last error if every step failed.
func (t *task) runSteps(ctx context.Context, steps []pipeline.Step, cur string) (string, error) {
	best := filepath.Join(t.dir, "best"+t.ext)
	var lastErr error
	ok := false
	for i, st := range steps {
		if err := ctx.Err(); err != nil {
			return cur, err
		}
		t.ev.Step = fmt.Sprintf("%s (%d/%d)", st.Tool, i+1, len(steps))
		t.ev.Elapsed = time.Since(t.start)
		t.e.publish(t.j, t.ev)

		dir := filepath.Join(t.dir, fmt.Sprintf("s%02d", i+1))
		cand, err := t.step(ctx, st, dir, cur)
		if err != nil {
			lastErr = err
		} else {
			ok = true
			if fi, err := os.Stat(cand); err == nil {
				if n := fi.Size(); n >= 8 && n < t.ev.Size && (st.Accept == nil || st.Accept(t.ev.Size, n)) {
					if os.Rename(cand, best) == nil {
						cur, t.ev.Size = best, n
					}
				}
			}
		}
		os.RemoveAll(dir)
	}
	if !ok {
		return cur, lastErr
	}
	return cur, nil
}

// step runs st in dir on a copy of cur and returns the path of its candidate result.
func (t *task) step(ctx context.Context, st pipeline.Step, dir, cur string) (string, error) {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", err
	}
	in, out := filepath.Join(dir, "in"+t.ext), filepath.Join(dir, "out"+t.ext)
	x := t.exec(dir)
	if st.Stdio {
		return out, x.RunStdio(ctx, st.Tool, cur, out, expand(st.Args, cur, out)...)
	}
	// Tools get a private copy: in-place tools modify it, and some {out} tools touch their input too.
	if err := copyFile(cur, in); err != nil {
		return "", err
	}
	if st.Func != nil {
		return out, st.Func(ctx, x, in, out)
	}
	if !slices.ContainsFunc(st.Args, func(a string) bool { return strings.Contains(a, "{out}") }) {
		out = in
	}
	return out, x.Run(ctx, st.Tool, expand(st.Args, in, out)...)
}

func expand(args []string, in, out string) []string {
	r := strings.NewReplacer("{in}", in, "{out}", out)
	res := make([]string, len(args))
	for i, a := range args {
		res[i] = r.Replace(a)
	}
	return res
}

func (t *task) exec(dir string) *pipeline.Exec {
	low := t.s.LowPriority
	return &pipeline.Exec{
		Dir: dir,
		Run: func(ctx context.Context, tool string, args ...string) error {
			return t.e.runTool(ctx, low, dir, tool, "", "", args)
		},
		RunStdio: func(ctx context.Context, tool, stdin, stdout string, args ...string) error {
			return t.e.runTool(ctx, low, dir, tool, stdin, stdout, args)
		},
	}
}

func (e *Engine) runTool(ctx context.Context, low bool, dir, tool, stdin, stdout string, args []string) error {
	argv, err := e.res.Command(tool)
	if err != nil {
		return fmt.Errorf("%s: %w", tool, err)
	}
	cmd := exec.CommandContext(ctx, argv[0], append(argv[1:len(argv):len(argv)], args...)...)
	cmd.Dir = dir
	cmd.SysProcAttr = procAttr(low)
	started, done := contain(cmd, low)
	defer done()
	// Grandchildren may inherit our pipes; don't hang on them after a kill.
	cmd.WaitDelay = 5 * time.Second
	var log capped
	cmd.Stdout, cmd.Stderr = &log, &log
	if stdin != "" {
		f, err := os.Open(stdin)
		if err != nil {
			return err
		}
		defer f.Close()
		cmd.Stdin = f
	}
	if stdout != "" {
		f, err := os.Create(stdout)
		if err != nil {
			return err
		}
		defer f.Close()
		cmd.Stdout = f
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", tool, err)
	}
	started()
	if err := cmd.Wait(); err != nil {
		if msg := strings.TrimSpace(log.String()); msg != "" {
			return fmt.Errorf("%s: %w: %s", tool, err, msg)
		}
		return fmt.Errorf("%s: %w", tool, err)
	}
	return nil
}

// capped keeps the first 4 KiB of tool output for error messages.
type capped struct{ bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	c.Buffer.Write(p[:min(len(p), max(0, 4096-c.Len()))])
	return len(p), nil
}

func selected(s *config.Settings, path string) bool {
	name := strings.ToLower(filepath.Base(path))
	match := func(pats []string) bool {
		return slices.ContainsFunc(pats, func(p string) bool {
			ok, _ := filepath.Match(strings.ToLower(p), name)
			return ok
		})
	}
	return (len(s.Include) == 0 || match(s.Include)) && !match(s.Exclude)
}

// variants are extensions that tools treat differently from the format's canonical one,
// e.g. ffmpeg picks the muxer from .m4a/.mov/.webm.
var variants = map[detect.Format][]string{
	detect.TIFF: {".dng"},
	detect.OGG:  {".oga", ".ogv", ".opus", ".spx"},
	detect.MP4:  {".m4a", ".m4v", ".m4b", ".mov", ".3gp", ".3g2"},
	detect.MKV:  {".mka", ".mk3d", ".webm"},
}

// tmpExt names temp files after the detected format, since tools pick codecs by extension:
// a PNG saved as .jpg must not be handed to magick as in.jpg, and pngquant --ext .png expects .png.
func tmpExt(path string, f detect.Format) string {
	if ext := strings.ToLower(filepath.Ext(path)); slices.Contains(variants[f], ext) {
		return ext
	}
	return "." + string(f)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}

var errModified = errors.New("modified during optimization")

// writeBack overwrites path with src in place, which keeps creation time, ACLs, hard links and
// attributes, and needs no extra space since src is smaller.
func writeBack(path string, was os.FileInfo, src, orig string, preserve bool) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Size() != was.Size() || !fi.ModTime().Equal(was.ModTime()) {
		return errModified
	}
	if err := overwrite(path, src); err != nil {
		if rerr := overwrite(path, orig); rerr != nil {
			return fmt.Errorf("%w; restoring original failed: %v", err, rerr)
		}
		return err
	}
	if preserve {
		return os.Chtimes(path, atime(was), was.ModTime())
	}
	return nil
}

func overwrite(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, in)
	if err == nil {
		err = out.Truncate(n)
	}
	if err == nil {
		err = out.Sync()
	}
	return errors.Join(err, out.Close())
}
