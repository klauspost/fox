//go:build unix

package optimize

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/klauspost/fox/internal/config"
)

func TestEnsurePrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tmp")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivate(dir); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("mode %v", fi.Mode())
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivate(link); err == nil {
		t.Error("symlink accepted")
	}
}

func TestTmpRoot(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	e := newEngine(&fakeResolver{}, config.Default(), hooks{})
	defer e.Close()
	dir, err := e.tmpBase()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cache, "fox", "tmp"); runtime.GOOS == "linux" && dir != want {
		t.Errorf("got %s, want %s", dir, want)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("stat %v, %v", fi, err)
	}

	// As under sudo with HOME kept: the home belongs to someone else.
	if os.Getuid() != 0 {
		t.Setenv("HOME", "/")
		if got, want := tmpRoot(), filepath.Join(os.TempDir(), "fox-"+strconv.Itoa(os.Getuid())); got != want {
			t.Errorf("foreign home: got %s, want %s", got, want)
		}
	}
}
