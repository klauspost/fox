// Command fox (File Optimizer X) shrinks files in place using a chain of external optimizers.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/klauspost/fox/internal/config"
	"github.com/klauspost/fox/internal/optimize"
	"github.com/klauspost/fox/internal/server"
	"github.com/klauspost/fox/internal/tools"
)

var version = "dev"

func main() { os.Exit(run()) }

func run() int {
	var (
		level       = flag.Int("level", 0, "optimization level, 1 (fast) to 9 (extreme) (default from settings)")
		workers     = flag.Int("j", 0, "files processed concurrently, 0 = number of physical CPU cores (default from settings)")
		lossy       = flag.Bool("lossy", false, "allow lossy optimization of all image formats")
		keep        = flag.Bool("keep-metadata", false, "keep metadata in all formats")
		noCache     = flag.Bool("no-cache", false, "also process files fox has already optimized")
		verbose     = flag.Bool("v", false, "print every step and skipped files")
		noBrowser   = flag.Bool("no-browser", false, "do not open a browser for the web UI")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "fox %s - File Optimizer X\n\n"+
			"Usage: fox [flags] [files or folders...]\n\n"+
			"Files are optimized in place. Without arguments fox opens its web UI.\n"+
			"Settings are read from ~/.fox/config.json; flags override them for this run only.\n\nFlags:\n", version)
		flag.PrintDefaults()
	}
	args, _ := parseArgs(flag.CommandLine, os.Args[1:]) // CommandLine exits on error.
	if *showVersion {
		fmt.Println("fox", version)
		return 0
	}
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["level"] && (*level < 1 || *level > 9) || *workers < 0 {
		fmt.Fprintln(os.Stderr, "fox: -level must be 1 to 9 and -j at least 0")
		return 2
	}

	s, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fox: warning: could not load settings:", err)
	}
	if len(args) == 0 {
		// The web UI saves engine settings, so command line overrides would leak into the settings file.
		for _, name := range []string{"level", "j", "lossy", "keep-metadata", "no-cache"} {
			if set[name] {
				fmt.Fprintf(os.Stderr, "fox: -%s only applies to files given on the command line; use the settings in the web UI\n", name)
			}
		}
	} else {
		images := []*config.Image{&s.JPEG.Image, &s.PNG, &s.GIF, &s.WebP}
		if set["level"] {
			s.Level = *level
		}
		if set["j"] {
			s.Workers = *workers
		}
		if set["lossy"] {
			for _, im := range images {
				im.Lossy = *lossy
			}
		}
		if set["keep-metadata"] {
			for _, im := range images {
				im.KeepMetadata = *keep
			}
			s.TIFF.KeepMetadata, s.BMP.KeepMetadata = *keep, *keep
			s.Audio.KeepMetadata, s.Video.KeepMetadata, s.Archive.KeepMetadata = *keep, *keep, *keep
		}
		if set["no-cache"] {
			s.SkipOptimized = !*noCache
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tools.EnsureRuntime(ctx, func(format string, a ...any) { fmt.Fprintf(os.Stderr, "fox: "+format+"\n", a...) })
	e := optimize.New(tools.NewResolver(), s)
	defer e.Close()

	if len(args) == 0 {
		if err := server.Run(ctx, e, !*noBrowser); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "fox:", err)
			return 1
		}
		return 0
	}

	// Restoring default signal handling lets a second Ctrl+C kill fox if cancellation hangs.
	defer context.AfterFunc(ctx, func() { stop(); e.Cancel(0) })()
	p := newPrinter(*verbose)
	defer e.Subscribe(p.event)()

	start := time.Now()
	statFailed := 0
	for _, arg := range args {
		if ctx.Err() != nil {
			break
		}
		paths, err := expand(arg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fox:", err)
			statFailed++
		}
		for _, path := range paths {
			e.Add(path, false)
		}
	}
	if ctx.Err() != nil {
		// Jobs queued by an Add that raced with the interrupt escaped the first Cancel.
		e.Cancel(0)
	}
	e.Wait()
	interrupted := ctx.Err() != nil

	failed := p.summary(e.Jobs(), statFailed, time.Since(start))
	switch {
	case interrupted:
		return 130
	case failed > 0:
		return 1
	}
	return 0
}

// parseArgs allows flags after file names ("fox a.png -v"); the flag package stops at the first
// non-flag. Everything after "--" is a file.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var files []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" {
			return append(files, rest...), nil
		}
		if len(rest) == 0 {
			return files, nil
		}
		files, args = append(files, rest[0]), rest[1:]
	}
}

// expand resolves wildcards, since cmd.exe and PowerShell pass them through unexpanded.
// Existing names win, so files with "[" in their name still work.
func expand(arg string) ([]string, error) {
	_, err := os.Stat(arg)
	switch {
	case err == nil:
		return []string{arg}, nil
	case !strings.ContainsAny(arg, "*?["):
		return nil, err
	}
	dir, pat := filepath.Split(arg)
	dirs := []string{dir}
	if strings.ContainsAny(dir, "*?[") {
		dirs, _ = filepath.Glob(filepath.Clean(dir))
	}
	var m []string
	for _, d := range dirs {
		es, _ := os.ReadDir(cmp.Or(d, "."))
		for _, e := range es {
			if ok, _ := filepath.Match(fold(pat), fold(e.Name())); ok {
				m = append(m, filepath.Join(d, e.Name()))
			}
		}
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("%s: no matching files", arg)
	}
	return m, nil
}

// fold matches Windows file names case-insensitively, as users expect "*.jpg" to find "IMG.JPG".
func fold(s string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(s)
	}
	return s
}

type printer struct {
	mu      sync.Mutex
	verbose bool
	cwd     string
	steps   map[int64]string
}

func newPrinter(verbose bool) *printer {
	cwd, _ := os.Getwd()
	return &printer{verbose: verbose, cwd: cwd, steps: map[int64]string{}}
}

func (p *printer) event(ev optimize.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	name := p.rel(ev.Path)
	switch ev.State {
	case optimize.Running:
		// Size updates repeat the current step; print each step once.
		if p.verbose && ev.Step != "" && p.steps[ev.ID] != ev.Step {
			p.steps[ev.ID] = ev.Step
			fmt.Printf("%8s  %s: %s\n", "·", name, ev.Step)
		}
	case optimize.Done:
		delete(p.steps, ev.ID)
		fmt.Printf("%8s  %10s → %10s  %6s  %s\n", percent(ev.Original, ev.Size), size(ev.Original), size(ev.Size), elapsed(ev.Elapsed), name)
		if p.verbose && ev.Message != "" {
			fmt.Printf("%8s  %s: %s\n", "note", name, ev.Message)
		}
	case optimize.Skipped:
		delete(p.steps, ev.ID)
		if p.verbose {
			fmt.Printf("%8s  %s: %s\n", "skipped", name, ev.Message)
		}
	case optimize.Failed:
		delete(p.steps, ev.ID)
		msg := ev.Message
		if !p.verbose {
			// Messages can carry kilobytes of tool output.
			msg, _, _ = strings.Cut(strings.TrimSpace(msg), "\n")
		}
		fmt.Printf("%8s  %s: %s\n", "FAILED", name, msg)
	}
}

// summary prints totals and returns failed plus the number of failed jobs.
func (p *printer) summary(jobs []optimize.Event, failed int, d time.Duration) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	var done, skipped int
	var orig, cur int64
	for _, j := range jobs {
		switch j.State {
		case optimize.Done:
			done++
			orig += j.Original
			cur += j.Size
		case optimize.Skipped:
			skipped++
		case optimize.Failed:
			failed++
		}
	}
	saved := orig - cur
	pct := 0.0
	if orig > 0 {
		pct = 100 * float64(saved) / float64(orig)
	}
	fmt.Printf("\n%d done, %d skipped, %d failed. Saved %s of %s (%.1f%%) in %s.\n",
		done, skipped, failed, size(saved), size(orig), pct, elapsed(d))
	return failed
}

func (p *printer) rel(path string) string {
	if r, err := filepath.Rel(p.cwd, path); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return r
	}
	return path
}

func percent(orig, cur int64) string {
	if orig <= 0 {
		return "0.0%"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(cur-orig)/float64(orig))
}

func size(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	f, unit := float64(n)/1024, "KB"
	for _, u := range []string{"MB", "GB", "TB"} {
		if f < 1024 {
			break
		}
		f, unit = f/1024, u
	}
	return fmt.Sprintf("%.2f %s", f, unit)
}

func elapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}
