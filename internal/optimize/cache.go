package optimize

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// cache remembers "<content hash> <settings fingerprint>" of files fox has produced.
type cache struct {
	path string
	mu   sync.Mutex
	set  map[string]bool
}

func (c *cache) loadLocked() {
	if c.set != nil {
		return
	}
	c.set = map[string]bool{}
	b, _ := os.ReadFile(c.path)
	for l := range strings.Lines(string(b)) {
		c.set[strings.TrimSpace(l)] = true
	}
}

func (c *cache) has(key string) bool {
	if c.path == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	return c.set[key]
}

func (c *cache) add(key string) error {
	if c.path == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked()
	if c.set[key] {
		return nil
	}
	c.set[key] = true
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	// Appending keeps concurrent fox processes from overwriting each other's entries.
	f, err := os.OpenFile(c.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(key + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}
