// Package tools locates the external optimizers, extracting embedded copies on demand.
package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
)

var ErrNotFound = errors.New("tool not available")

// Tool describes an external program.
type Tool struct {
	Name string
	// Files are the bundled (Windows) files; Files[0] is the executable, the rest are dependencies.
	Files []string
	// Unix is the command to look for in PATH on other systems. Empty means the tool is never taken from PATH,
	// on any system: either no compatible build is commonly installed, or the usual one is a different program.
	// Host-only tools are the exception: on Windows they are looked up in PATH as Files[0].
	Unix string
	// URL is the project home page.
	URL string
}

// Manifest describes one group of the embedded bundle, bundle/<group>/manifest.json; it is written by the pack command.
type Manifest struct {
	// Hash identifies the group's contents.
	Hash  string                  `json:"hash"`
	Files map[string]ManifestFile `json:"files"`
}

type ManifestFile struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	group  string // directory of the .zst within the bundle
}

type result struct {
	argv []string
	err  error
}

// Resolver turns tool names into command lines.
type Resolver struct {
	bundle   fs.FS
	manifest func() *Manifest
	dir      func() (string, error)
	lock     *os.File // held open for the life of the process, see lockDir
	locks    sync.Map // file name -> *sync.Mutex

	mu        sync.Mutex
	gen       int
	overrides map[string][]string
	cache     map[string]result
}

// NewResolver creates a resolver. There should be one per process, since it tracks extraction.
func NewResolver() *Resolver { return newResolver(bundleFS, defaultRoots()) }

// newResolver extracts into <root>/<hash> of the first writable root.
func newResolver(bundle fs.FS, roots []string) *Resolver {
	r := &Resolver{bundle: bundle, cache: map[string]result{}}
	r.manifest = sync.OnceValue(func() *Manifest { return loadManifest(bundle) })
	r.dir = sync.OnceValues(func() (dir string, err error) {
		dir, r.lock, err = pickDir(roots, r.manifest().Hash)
		return dir, err
	})
	return r
}

// loadManifest merges the manifests of the embedded groups. Its Hash, which names the extraction directory, covers
// all groups, so the full and the lite build extract to different directories.
func loadManifest(bundle fs.FS) *Manifest {
	paths, _ := fs.Glob(bundle, "*/manifest.json")
	m := &Manifest{Files: map[string]ManifestFile{}}
	h := sha256.New()
	for _, p := range paths {
		b, err := fs.ReadFile(bundle, p)
		var g Manifest
		if err != nil || json.Unmarshal(b, &g) != nil || g.Hash == "" {
			continue // half-written by an interrupted pack
		}
		group := path.Dir(p)
		fmt.Fprintln(h, group, g.Hash)
		for name, f := range g.Files {
			f.group = group
			m.Files[name] = f
		}
	}
	if len(m.Files) == 0 {
		return nil // no bundle on this platform
	}
	m.Hash = hex.EncodeToString(h.Sum(nil)[:8])
	return m
}

func defaultRoots() []string {
	var roots []string
	if exe, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Join(filepath.Dir(exe), "fox-tools"))
	}
	if dir, err := os.UserCacheDir(); err == nil {
		roots = append(roots, filepath.Join(dir, "fox", "tools"))
	}
	return roots
}

// SetOverrides maps tool names to explicit command prefixes, e.g. {"pngout": ["wine", "pngout.exe"]}.
func (r *Resolver) SetOverrides(m map[string][]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overrides = make(map[string][]string, len(m))
	for k, v := range m {
		if len(v) > 0 {
			r.overrides[k] = slices.Clone(v)
		}
	}
	r.cache = map[string]result{}
	r.gen++
}

// Command returns the argv prefix (executable plus fixed args) to run the named tool.
// Embedded tools are extracted on first use. Returns ErrNotFound if unavailable.
func (r *Resolver) Command(name string) ([]string, error) {
	r.mu.Lock()
	c, ok := r.cache[name]
	ov, gen := r.overrides[name], r.gen
	r.mu.Unlock()
	if !ok {
		c.argv, c.err = r.resolve(name, ov)
		// Extraction failures may be transient (disk full, AV lock); only definitive answers are cached.
		if c.err == nil || errors.Is(c.err, ErrNotFound) {
			r.mu.Lock()
			if r.gen == gen {
				r.cache[name] = c
			}
			r.mu.Unlock()
		}
	}
	// Callers append arguments; never hand out the cached backing array.
	return slices.Clone(c.argv), c.err
}

// Available reports whether the tool can be run, without extracting it.
func (r *Resolver) Available(name string) bool {
	r.mu.Lock()
	c, ok := r.cache[name]
	ov := r.overrides[name]
	r.mu.Unlock()
	switch t, known := All[name]; {
	case ok:
		return c.err == nil
	case ov != nil:
		_, err := exec.LookPath(ov[0])
		return err == nil
	case !known:
		return false
	case r.bundled(t):
		return true
	default:
		_, err := lookPath(t)
		return err == nil
	}
}

func (r *Resolver) resolve(name string, ov []string) ([]string, error) {
	if ov != nil {
		if _, err := exec.LookPath(ov[0]); err != nil {
			return nil, fmt.Errorf("%s: override %q: %w", name, ov[0], ErrNotFound)
		}
		return ov, nil
	}
	t, ok := All[name]
	if !ok {
		return nil, fmt.Errorf("%s: unknown tool: %w", name, ErrNotFound)
	}
	if r.bundled(t) {
		dir, err := r.extract(t)
		if err != nil {
			return nil, fmt.Errorf("%s: extract: %w", name, err)
		}
		return []string{filepath.Join(dir, t.Files[0])}, nil
	}
	p, err := lookPath(t)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return []string{p}, nil
}

func lookPath(t Tool) (string, error) {
	if t.Unix == "" && (!t.HostOnly() || runtime.GOOS != "windows") {
		return "", ErrNotFound
	}
	if runtime.GOOS != "windows" {
		return exec.LookPath(t.Unix)
	}
	// The bundled file name is the most likely name of a user-installed copy (gswin64c...).
	p, err := exec.LookPath(t.Files[0])
	if err != nil && t.Name == "gs" {
		// The Ghostscript installer does not add itself to PATH.
		return newestGhostscript(os.Getenv("ProgramFiles"))
	}
	return p, err
}

func newestGhostscript(programFiles string) (string, error) {
	if programFiles == "" {
		return "", ErrNotFound
	}
	m, _ := filepath.Glob(filepath.Join(programFiles, "gs", "gs*", "bin", "gswin64c.exe"))
	if len(m) == 0 {
		return "", ErrNotFound
	}
	ver := func(p string) []int {
		var v []int
		for s := range strings.SplitSeq(strings.TrimPrefix(filepath.Base(filepath.Dir(filepath.Dir(p))), "gs"), ".") {
			n, _ := strconv.Atoi(s)
			v = append(v, n)
		}
		return v
	}
	return slices.MaxFunc(m, func(a, b string) int { return slices.Compare(ver(a), ver(b)) }), nil
}

func (r *Resolver) bundled(t Tool) bool {
	m := r.manifest()
	if m == nil {
		return false
	}
	for _, f := range t.Files {
		if _, ok := m.Files[f]; !ok {
			return false
		}
	}
	return true
}
