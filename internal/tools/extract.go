package tools

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

func (r *Resolver) extract(t Tool) (string, error) {
	dir, err := r.dir()
	if err != nil {
		return "", err
	}
	m := r.manifest()
	for _, f := range t.Files {
		if err := r.extractFile(dir, f, m.Files[f].Size); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// extractFile is safe against concurrent goroutines (per-file lock) and concurrent fox processes
// (decompress to a unique temp name, then rename).
func (r *Resolver) extractFile(dir, name string, size int64) error {
	l, _ := r.locks.LoadOrStore(name, new(sync.Mutex))
	l.(*sync.Mutex).Lock()
	defer l.(*sync.Mutex).Unlock()

	dst := filepath.Join(dir, name)
	if sameSize(dst, size) {
		return nil
	}
	src, err := r.bundle.Open(name + ".zst")
	if err != nil {
		return err
	}
	defer src.Close()
	dec, err := zstd.NewReader(src, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return err
	}
	defer dec.Close()

	tmp, err := os.CreateTemp(dir, name+".*.tmp")
	if err != nil {
		return err
	}
	n, err := io.Copy(tmp, dec)
	err = errors.Join(err, tmp.Chmod(0o755), tmp.Close())
	if err == nil && n != size {
		err = fmt.Errorf("%s: extracted %d bytes, want %d", name, n, size)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dst)
	}
	if err != nil {
		os.Remove(tmp.Name())
		// Windows refuses to replace a file that is running or being renamed; another process got there first.
		if sameSize(dst, size) {
			return nil
		}
	}
	return err
}

func sameSize(path string, size int64) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Size() == size
}

const lockName = "fox.lock"

// pickDir returns <root>/<hash> for the first writable root, with its lock, and removes other bundle versions there.
func pickDir(roots []string, hash string) (string, *os.File, error) {
	for _, root := range roots {
		dir := filepath.Join(root, hash)
		if lock, err := lockDir(dir); err == nil {
			removeStale(root, hash)
			removeTemps(dir)
			return dir, lock, nil
		}
	}
	return "", nil, fmt.Errorf("no writable directory for tools in %q", roots)
}

// lockDir creates dir and opens its lock file, which doubles as the writability check. Windows refuses to delete
// an open file, which is how removeStale in another fox (other version, or fox-lite next to fox) sees dir is in use.
func lockDir(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, lockName), os.O_RDWR|os.O_CREATE, 0o644)
}

// removeStale deletes directories of other bundles that no running fox holds.
func removeStale(root, keep string) {
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if !e.IsDir() || e.Name() == keep || !isHash(e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if err := os.Remove(filepath.Join(dir, lockName)); err == nil || errors.Is(err, fs.ErrNotExist) {
			os.RemoveAll(dir)
		}
	}
}

// removeTemps deletes leftovers of killed extractions. Recent ones may belong to a fox that is extracting right now.
func removeTemps(dir string) {
	tmps, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	for _, t := range tmps {
		if fi, err := os.Stat(t); err == nil && time.Since(fi.ModTime()) > time.Hour {
			os.Remove(t)
		}
	}
}

func isHash(s string) bool {
	_, err := hex.DecodeString(s)
	return len(s) == 16 && err == nil
}
