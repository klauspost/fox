package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/klauspost/compress/zstd"
)

const testHash = "0123456789abcdef"

// fakeBundle returns a bundle with files in its core group.
func fakeBundle(t *testing.T, files map[string][]byte) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	addGroup(t, fsys, "core", testHash, files)
	return fsys
}

func addGroup(t *testing.T, fsys fstest.MapFS, group, hash string, files map[string][]byte) {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{Hash: hash, Files: map[string]ManifestFile{}}
	for name, b := range files {
		fsys[group+"/"+name+".zst"] = &fstest.MapFile{Data: enc.EncodeAll(b, nil)}
		m.Files[name] = ManifestFile{Size: int64(len(b))}
	}
	b, _ := json.Marshal(m)
	fsys[group+"/manifest.json"] = &fstest.MapFile{Data: b}
}

// testDir returns the extraction directory of every fakeBundle: it only depends on the group hashes.
func testDir(t *testing.T) string {
	return loadManifest(fakeBundle(t, map[string][]byte{"x": nil})).Hash
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rand.IntN(16)) // compressible but not trivially
	}
	return b
}

// fakePath puts an empty "executable" for t on a fresh PATH and returns its directory.
func fakePath(t *testing.T, tool Tool) string {
	t.Helper()
	dir := t.TempDir()
	name := tool.Unix
	if runtime.GOOS == "windows" {
		name = tool.Files[0]
	}
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return dir
}

func TestResolutionOrder(t *testing.T) {
	pathDir := fakePath(t, All["oxipng"])
	root := t.TempDir()
	r := testResolver(t, fakeBundle(t, map[string][]byte{"oxipng.exe": []byte("embedded")}), []string{root})

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r.SetOverrides(map[string][]string{"oxipng": {self, "--flag"}})
	if got, err := r.Command("oxipng"); err != nil || !slices.Equal(got, []string{self, "--flag"}) {
		t.Fatalf("override: got %q, %v", got, err)
	}

	r.SetOverrides(nil)
	want := filepath.Join(root, testDir(t), "oxipng.exe")
	got, err := r.Command("oxipng")
	if err != nil || !slices.Equal(got, []string{want}) {
		t.Fatalf("bundle: got %q, %v; want %q", got, err, want)
	}
	if b, _ := os.ReadFile(want); string(b) != "embedded" {
		t.Fatalf("extracted content %q", b)
	}
	got[0] = "mutated"
	if again, _ := r.Command("oxipng"); again[0] != want {
		t.Fatalf("cached argv was mutated by caller: %q", again)
	}

	r = testResolver(t, fstest.MapFS{}, []string{t.TempDir()})
	got, err = r.Command("oxipng")
	if err != nil || len(got) != 1 || filepath.Dir(got[0]) != pathDir {
		t.Fatalf("PATH: got %q, %v; want in %s", got, err, pathDir)
	}
}

func TestNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("ProgramFiles", t.TempDir()) // a host Ghostscript would satisfy "gs"
	r := testResolver(t, fstest.MapFS{}, []string{t.TempDir()})
	r.SetOverrides(map[string][]string{"pngout": {filepath.Join(t.TempDir(), "missing.exe")}})
	for _, name := range []string{"oxipng", "pngout", "no-such-tool"} {
		if _, err := r.Command(name); !errors.Is(err, ErrNotFound) {
			t.Errorf("Command(%q) = %v, want ErrNotFound", name, err)
		}
		if r.Available(name) {
			t.Errorf("Available(%q) = true", name)
		}
	}
	// A partially packed bundle must not be used.
	r = testResolver(t, fakeBundle(t, map[string][]byte{"gswin64c.exe": []byte("x")}), []string{t.TempDir()})
	if _, err := r.Command("gs"); !errors.Is(err, ErrNotFound) {
		t.Errorf("incomplete bundle: %v", err)
	}
}

// A gzip in PATH is GNU gzip (on Windows too, via Git), which rejects libdeflate's "-12".
func TestNoPathWithoutUnixName(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"gzip", "gzip.exe"} {
		os.WriteFile(filepath.Join(dir, f), nil, 0o755)
	}
	t.Setenv("PATH", dir)
	r := testResolver(t, fstest.MapFS{}, []string{t.TempDir()})
	if argv, err := r.Command("gzip"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("gzip resolved from PATH: %q, %v", argv, err)
	}
	if r.Available("gzip") {
		t.Fatal("gzip available from PATH")
	}
}

// pingo may not be redistributed: only a copy the user installed counts, which needs a Windows build.
func TestHostOnly(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"pingo", "pingo.exe"} {
		os.WriteFile(filepath.Join(dir, f), nil, 0o755)
	}
	t.Setenv("PATH", dir)
	r := testResolver(t, fstest.MapFS{}, []string{t.TempDir()})
	want := runtime.GOOS == "windows"
	if argv, err := r.Command("pingo"); (err == nil) != want || r.Available("pingo") != want {
		t.Fatalf("pingo from PATH: %q, %v; want found = %v", argv, err, want)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r.SetOverrides(map[string][]string{"pingo": {self}})
	if argv, err := r.Command("pingo"); err != nil || argv[0] != self {
		t.Fatalf("override: got %q, %v", argv, err)
	}
}

func TestGhostscriptProgramFiles(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	pf := t.TempDir()
	for _, v := range []string{"gs9.56.1", "gs10.05.1", "gs10.4"} {
		bin := filepath.Join(pf, "gs", v, "bin")
		os.MkdirAll(bin, 0o755)
		os.WriteFile(filepath.Join(bin, "gswin64c.exe"), nil, 0o755)
	}
	t.Setenv("PATH", t.TempDir())
	t.Setenv("ProgramFiles", pf)
	r := testResolver(t, fstest.MapFS{}, []string{t.TempDir()})
	want := filepath.Join(pf, "gs", "gs10.05.1", "bin", "gswin64c.exe")
	if argv, err := r.Command("gs"); err != nil || argv[0] != want {
		t.Fatalf("got %q, %v; want %s", argv, err, want)
	}
	t.Setenv("ProgramFiles", t.TempDir())
	if r := testResolver(t, fstest.MapFS{}, []string{t.TempDir()}); r.Available("gs") {
		t.Fatal("gs available without an installation")
	}
}

func TestAvailableDoesNotExtract(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	root := filepath.Join(t.TempDir(), "fox-tools")
	r := testResolver(t, fakeBundle(t, map[string][]byte{"oxipng.exe": []byte("x")}), []string{root})
	if !r.Available("oxipng") || r.Available("pngout") {
		t.Fatal("wrong availability")
	}
	if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Available touched disk: %v", err)
	}
}

func TestExtractConcurrent(t *testing.T) {
	files := map[string][]byte{}
	for _, name := range slices.Concat(All["gs"].Files, All["pngwolf"].Files) {
		files[name] = randBytes(256<<10 + rand.IntN(1000))
	}
	bundle := fakeBundle(t, files)
	root := t.TempDir()
	// Two resolvers sharing a root stand in for two fox processes.
	rs := []*Resolver{testResolver(t, bundle, []string{root}), testResolver(t, bundle, []string{root})}

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := range 64 {
		wg.Go(func() {
			tool := []string{"gs", "pngwolf"}[i%2]
			argv, err := rs[i%3%2].Command(tool)
			if err == nil && filepath.Base(argv[0]) != All[tool].Files[0] {
				err = fmt.Errorf("%s: unexpected argv %q", tool, argv)
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(root, testDir(t))
	for name, want := range files {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: content mismatch (%v)", name, err)
		}
	}
	tmps, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(tmps) > 0 {
		t.Errorf("leftover temp files: %q", tmps)
	}
}

func TestReuseBySize(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, testDir(t))
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "oxipng.exe"), []byte("SAMESIZE"), 0o755)
	os.WriteFile(filepath.Join(dir, "pngout.exe"), []byte("old"), 0o755)

	r := testResolver(t, fakeBundle(t, map[string][]byte{
		"oxipng.exe": []byte("embedded"),
		"pngout.exe": []byte("new content"),
	}), []string{root})
	for _, name := range []string{"oxipng", "pngout"} {
		if _, err := r.Command(name); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "oxipng.exe")); string(b) != "SAMESIZE" {
		t.Errorf("file with matching size was rewritten: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "pngout.exe")); string(b) != "new content" {
		t.Errorf("file with wrong size was not replaced: %q", b)
	}
}

func TestStaleCleanupAndFallback(t *testing.T) {
	// A root below a regular file can never be created, forcing the fallback.
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0o644)
	root := t.TempDir()
	stale := filepath.Join(root, "fedcba9876543210")
	os.MkdirAll(stale, 0o755)
	os.WriteFile(filepath.Join(stale, "old.exe"), []byte("x"), 0o644)
	other := filepath.Join(root, "not-a-hash")
	os.MkdirAll(other, 0o755)
	cur := filepath.Join(root, testDir(t))
	os.MkdirAll(cur, 0o755)
	oldTmp, newTmp := filepath.Join(cur, "mutool.exe.1.tmp"), filepath.Join(cur, "mutool.exe.2.tmp")
	os.WriteFile(oldTmp, nil, 0o644)
	os.WriteFile(newTmp, nil, 0o644)
	os.Chtimes(oldTmp, time.Time{}, time.Now().Add(-2*time.Hour))

	r := testResolver(t, fakeBundle(t, map[string][]byte{"oxipng.exe": []byte("x")}), []string{filepath.Join(blocker, "fox-tools"), root})
	argv, err := r.Command("oxipng")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, testDir(t), "oxipng.exe"); argv[0] != want {
		t.Errorf("got %q, want fallback %q", argv[0], want)
	}
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stale dir not removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("unrelated dir removed: %v", err)
	}
	if _, err := os.Stat(oldTmp); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("abandoned temp file not removed: %v", err)
	}
	if _, err := os.Stat(newTmp); err != nil {
		t.Errorf("recent temp file removed: %v", err)
	}
}

func TestRegistry(t *testing.T) {
	for name, tool := range All {
		if tool.Name != name || len(tool.Files) == 0 || tool.URL == "" {
			t.Errorf("bad entry %q: %+v", name, tool)
		}
	}
	plugins := filepath.Join("..", "..", "_ref", "Win64", "Debug", "Plugins64")
	if _, err := os.Stat(plugins); err != nil {
		t.Skip("reference tools not present")
	}
	for _, tool := range All {
		for _, f := range tool.Files {
			if _, err := os.Stat(filepath.Join(plugins, f)); err != nil {
				t.Errorf("%s: %v", tool.Name, err)
			}
		}
	}
}

// TestRealBundle runs tools from the embedded bundle.
func TestRealBundle(t *testing.T) {
	if loadManifest(bundleFS) == nil {
		t.Skip("no bundle on " + runtime.GOOS)
	}
	root := t.TempDir()
	r := testResolver(t, bundleFS, []string{root})
	argv, err := r.Command("oxipng")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(argv[0], "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "oxipng") {
		t.Fatalf("oxipng --version: %v: %s", err, out)
	}
	t.Logf("%s", bytes.TrimSpace(out))

	if !r.bundled(All["gs"]) {
		return // lite build
	}
	argv, err = r.Command("gs")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range All["gs"].Files {
		if _, err := os.Stat(filepath.Join(filepath.Dir(argv[0]), f)); err != nil {
			t.Error(err)
		}
	}
	out, err = exec.Command(argv[0], "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("gs --version: %v: %s", err, out)
	}
	t.Logf("gs %s", bytes.TrimSpace(out))
}

// TestBundleIntegrity checks the embedded bundle against its manifests.
func TestBundleIntegrity(t *testing.T) {
	m := loadManifest(bundleFS)
	if m == nil {
		t.Skip("no bundle on " + runtime.GOOS)
	}
	r := testResolver(t, bundleFS, nil)
	full := r.bundled(All["gs"])
	for _, tool := range All {
		if want := !tool.HostOnly() && (full || !tool.Extra()); r.bundled(tool) != want {
			t.Errorf("%s: bundled = %v, want %v (build with extra tools = %v)", tool.Name, r.bundled(tool), want, full)
		}
	}
	zsts, _ := fs.Glob(bundleFS, "*/*.zst")
	if len(zsts) != len(m.Files) {
		t.Errorf("%d .zst files for %d manifest entries", len(zsts), len(m.Files))
	}
	if testing.Short() {
		t.Skip("decompressing the bundle")
	}
	for name, f := range m.Files {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			zf, err := bundleFS.Open(path.Join(f.group, name+".zst"))
			if err != nil {
				t.Fatal(err)
			}
			defer zf.Close()
			dec, err := zstd.NewReader(zf, zstd.WithDecoderConcurrency(1))
			if err != nil {
				t.Fatal(err)
			}
			defer dec.Close()
			h := sha256.New()
			n, err := io.Copy(h, dec)
			if sum := hex.EncodeToString(h.Sum(nil)); err != nil || n != f.Size || sum != f.SHA256 {
				t.Errorf("got %d bytes, sha256 %s, %v; manifest has %d bytes, sha256 %s", n, sum, err, f.Size, f.SHA256)
			}
		})
	}
}

// TestBundleGroups checks the committed manifests against the registry, whichever groups this build embeds.
func TestBundleGroups(t *testing.T) {
	m := loadManifest(os.DirFS("bundle"))
	if m == nil {
		t.Fatal("no manifests in bundle/")
	}
	n := 0
	for _, g := range []string{"core", "extra"} {
		b, err := os.ReadFile(filepath.Join("bundle", g, "manifest.json"))
		var gm Manifest
		if err != nil || json.Unmarshal(b, &gm) != nil {
			t.Fatalf("%s: %v", g, err)
		}
		n += len(gm.Files)
	}
	if n != len(m.Files) {
		t.Errorf("%d files are in both groups", n-len(m.Files))
	}
	for _, tool := range All {
		for _, f := range tool.Files {
			if _, ok := m.Files[f]; !ok && !tool.HostOnly() {
				t.Errorf("%s: %s not bundled", tool.Name, f)
			}
		}
	}
	// A file belongs to extra exactly when only extra tools use it: anything else would be missing from or bloat the lite build.
	// Files of host-only tools have no users here, so they must not be bundled at all.
	for name, f := range m.Files {
		users, extra := 0, true
		for _, tool := range All {
			if slices.Contains(tool.Files, name) && !tool.HostOnly() {
				users++
				extra = extra && tool.Extra()
			}
		}
		want := "core"
		if extra {
			want = "extra"
		}
		if users == 0 || f.group != want {
			t.Errorf("%s: in %s, used by %d tools, want %s", name, f.group, users, want)
		}
	}
}

func TestGroups(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	bundle := fakeBundle(t, map[string][]byte{"oxipng.exe": []byte("core")})
	addGroup(t, bundle, "extra", "fedcba9876543210", map[string][]byte{"magick.exe": []byte("extra")})
	root := t.TempDir()
	r := testResolver(t, bundle, []string{root})
	for tool, want := range map[string]string{"oxipng": "core", "magick": "extra"} {
		argv, err := r.Command(tool)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(argv[0]); string(b) != want || filepath.Dir(argv[0]) == filepath.Join(root, testDir(t)) {
			t.Errorf("%s: got %q in %s; want %q outside the core-only directory", tool, b, argv[0], want)
		}
	}
	if r := testResolver(t, fakeBundle(t, nil), []string{t.TempDir()}); r.Available("magick") {
		t.Error("magick available without the extra group")
	}
}

// TestStaleKeepsInUse stands in for fox-lite and fox (same core, plus extra) sharing fox-tools.
func TestStaleKeepsInUse(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("bundles are Windows only")
	}
	root := t.TempDir()
	core := map[string][]byte{"oxipng.exe": []byte("core")}
	lite, full := fakeBundle(t, core), fakeBundle(t, core)
	addGroup(t, full, "extra", "fedcba9876543210", map[string][]byte{"magick.exe": []byte("extra")})

	r1 := testResolver(t, lite, []string{root})
	argv, err := r1.Command("oxipng")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testResolver(t, full, []string{root}).Command("oxipng"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(argv[0]); err != nil {
		t.Fatalf("tools of a running fox were deleted: %v", err)
	}
	r1.lock.Close()
	if _, err := testResolver(t, full, []string{root}).Command("oxipng"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(argv[0])); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("directory of an exited fox kept: %v", err)
	}
}

// testResolver releases the directory lock before t's temp dirs are removed, which Windows would refuse.
func testResolver(t *testing.T, bundle fs.FS, roots []string) *Resolver {
	r := newResolver(bundle, roots)
	t.Cleanup(func() {
		if r.lock != nil {
			r.lock.Close()
		}
	})
	return r
}
