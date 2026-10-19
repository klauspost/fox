// Command pack compresses the tool files listed in tools.All into internal/tools/bundle for embedding.
// Files used only by extra tools go to bundle/extra, which the lite build leaves out; all others to bundle/core.
// Host-only tools are skipped: their licenses forbid redistribution.
// Existing .zst files whose content still matches the source are kept, so re-packing is cheap.
//
//	go run ./internal/tools/pack -src _ref/Win64/Debug/Plugins64
package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/klauspost/fox/internal/tools"
)

// maxZst keeps the committed files clear of GitHub's 100 MB per-file limit.
var maxZst int64 = 90e6

var groups = []string{"core", "extra"}

func main() {
	src := flag.String("src", "", "directory containing the tool files (required)")
	out := flag.String("out", filepath.Join("internal", "tools", "bundle"), "bundle directory")
	level := flag.Int("level", 19, "zstd compression level, 1-22 (files reused from an earlier pack keep their level)")
	flag.Parse()
	if *src == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := pack(*src, *out, zstd.EncoderLevelFromZstd(*level)); err != nil {
		log.Fatal(err)
	}
}

func pack(src, out string, level zstd.EncoderLevel) error {
	// Files shared with a core tool (vcruntime140_1.dll) must be in core, or the lite build would lack them.
	group := map[string]string{}
	for _, t := range tools.All {
		if t.HostOnly() {
			continue
		}
		for _, f := range t.Files {
			if !t.Extra() {
				group[f] = "core"
			} else if group[f] == "" {
				group[f] = "extra"
			}
		}
	}
	var names []string
	for _, n := range slices.Sorted(maps.Keys(group)) {
		if _, err := os.Stat(filepath.Join(src, n)); err != nil {
			log.Printf("warning: %s missing: %v", n, err)
			continue
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		// Don't replace a working bundle with an empty one because of a wrong -src.
		return fmt.Errorf("no tool files found in %s", src)
	}

	for _, g := range groups {
		dir := filepath.Join(out, g)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		// Without a manifest the group is unused, so an interrupted pack can't produce a mismatched one.
		if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	old, _ := filepath.Glob(filepath.Join(out, "*", "*.zst"))
	for _, f := range old {
		n := strings.TrimSuffix(filepath.Base(f), ".zst")
		var err error
		switch _, found := slices.BinarySearch(names, n); {
		case !found:
			err = os.Remove(f)
		case filepath.Base(filepath.Dir(f)) != group[n]:
			// Moved to the other group by a registry change: packFile reuses it if it still matches.
			err = os.Rename(f, filepath.Join(out, group[n], n+".zst"))
		}
		if err != nil {
			return err
		}
	}

	// Largest first, so the long ffmpeg/magick compressions don't end up running alone at the end.
	sizes := map[string]int64{}
	for _, n := range names {
		fi, _ := os.Stat(filepath.Join(src, n))
		sizes[n] = fi.Size()
	}
	slices.SortFunc(names, func(a, b string) int { return cmp.Compare(sizes[b], sizes[a]) })

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		errs   []error
		files  = map[string]map[string]tools.ManifestFile{}
		comp   = map[string]int64{}
		reused = map[string]int{}
		sem    = make(chan struct{}, runtime.NumCPU())
	)
	for _, g := range groups {
		files[g] = map[string]tools.ManifestFile{}
	}
	for _, n := range names {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			g := group[n]
			mf, csize, kept, err := packFile(filepath.Join(src, n), filepath.Join(out, g, n+".zst"), level)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", n, err))
				return
			}
			files[g][n] = mf
			comp[g] += csize
			if kept {
				reused[g]++
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return err
	}

	for _, g := range groups {
		h := sha256.New()
		for _, n := range slices.Sorted(maps.Keys(files[g])) {
			fmt.Fprintf(h, "%s\x00%s\n", n, files[g][n].SHA256)
		}
		m := tools.Manifest{Hash: hex.EncodeToString(h.Sum(nil)[:8]), Files: files[g]}
		b, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, g, "manifest.json"), b, 0o644); err != nil {
			return err
		}
		var raw int64
		for _, f := range files[g] {
			raw += f.Size
		}
		log.Printf("%s: packed %d files (%d reused), %.1f MB -> %.1f MB, hash %s", g, len(files[g]), reused[g], mb(raw), mb(comp[g]), m.Hash)
	}
	return nil
}

// packFile compresses src to dst, unless dst already decompresses to exactly src.
func packFile(src, dst string, level zstd.EncoderLevel) (mf tools.ManifestFile, csize int64, reused bool, err error) {
	if mf, err = digestFile(src); err != nil {
		return mf, 0, false, err
	}
	if have, csize, err := digestZst(dst); err == nil && have == mf && csize <= maxZst {
		return mf, csize, true, nil
	}
	in, err := os.Open(src)
	if err != nil {
		return mf, 0, false, err
	}
	defer in.Close()
	f, err := os.Create(dst)
	if err != nil {
		return mf, 0, false, err
	}
	lw := &limitWriter{w: f, left: maxZst}
	// Files are already compressed in parallel; one encoder goroutine each keeps memory in check.
	enc, err := zstd.NewWriter(lw, zstd.WithEncoderLevel(level), zstd.WithEncoderConcurrency(1))
	if err == nil {
		_, err = io.Copy(enc, in)
		err = errors.Join(err, enc.Close())
	}
	if err = errors.Join(err, f.Close()); err != nil {
		os.Remove(dst)
	}
	return mf, maxZst - lw.left, false, err
}

type limitWriter struct {
	w    io.Writer
	left int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.left {
		return 0, fmt.Errorf("compressed size over %d bytes, too close to GitHub's 100 MB file limit", maxZst)
	}
	l.left -= int64(len(p))
	return l.w.Write(p)
}

func digestFile(path string) (tools.ManifestFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return tools.ManifestFile{}, err
	}
	defer f.Close()
	return digest(f)
}

// digestZst returns the digest of the decompressed content and the compressed size.
func digestZst(path string) (tools.ManifestFile, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return tools.ManifestFile{}, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return tools.ManifestFile{}, 0, err
	}
	dec, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return tools.ManifestFile{}, 0, err
	}
	defer dec.Close()
	mf, err := digest(dec)
	return mf, fi.Size(), err
}

func digest(r io.Reader) (tools.ManifestFile, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	return tools.ManifestFile{Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, err
}

func mb(n int64) float64 { return float64(n) / (1 << 20) }
