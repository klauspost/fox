package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
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

func fakeBundle(t *testing.T, files map[string][]byte) fstest.MapFS {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	m := Manifest{Hash: testHash, Files: map[string]ManifestFile{}}
	for name, b := range files {
		fsys[name+".zst"] = &fstest.MapFile{Data: enc.EncodeAll(b, nil)}
		m.Files[name] = ManifestFile{Size: int64(len(b))}
	}
	b, _ := json.Marshal(m)
	fsys["manifest.json"] = &fstest.MapFile{Data: b}
	return fsys
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
	want := filepath.Join(root, testHash, "oxipng.exe")
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
	dir := filepath.Join(root, testHash)
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
	dir := filepath.Join(root, testHash)
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
	cur := filepath.Join(root, testHash)
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
	if want := filepath.Join(root, testHash, "oxipng.exe"); argv[0] != want {
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

// TestRealBundle checks the embedded bundle, when one has been packed.
func TestRealBundle(t *testing.T) {
	if _, err := fs.Stat(bundleFS, "manifest.json"); err != nil {
		t.Skip("no packed bundle; run `go tool task bundle`")
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

// TestStaleKeepsInUse stands in for fox and fox-lite (different bundle hashes) sharing fox-tools.
func TestStaleKeepsInUse(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("bundles are Windows only")
	}
	root := t.TempDir()
	lite := fakeBundle(t, map[string][]byte{"oxipng.exe": []byte("lite")})
	b, _ := json.Marshal(Manifest{Hash: "fedcba9876543210", Files: map[string]ManifestFile{"oxipng.exe": {Size: 4}}})
	lite["manifest.json"] = &fstest.MapFile{Data: b}
	full := fakeBundle(t, map[string][]byte{"oxipng.exe": []byte("full")})

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
