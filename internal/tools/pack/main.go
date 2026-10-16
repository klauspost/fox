// Command pack compresses the tool files listed in tools.All into internal/tools/bundle for embedding.
// Existing .zst files whose content still matches the source are kept, so re-packing is cheap.
//
//	go run ./internal/tools/pack -src _ref/Win64/Debug/Plugins64 [-exclude magick,gs]
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

func main() {
	src := flag.String("src", "", "directory containing the tool files (required)")
	out := flag.String("out", filepath.Join("internal", "tools", "bundle"), "bundle directory")
	level := flag.Int("level", 19, "zstd compression level, 1-22 (files reused from an earlier pack keep their level)")
	exclude := flag.String("exclude", "", "comma-separated tool names to leave out, e.g. magick,gs")
	flag.Parse()
	if *src == "" {
		flag.Usage()
		os.Exit(2)
	}
	var ex []string
	if *exclude != "" {
		ex = strings.Split(*exclude, ",")
	}
	if err := pack(*src, *out, zstd.EncoderLevelFromZstd(*level), ex); err != nil {
		log.Fatal(err)
	}
}

func pack(src, out string, level zstd.EncoderLevel, exclude []string) error {
	for _, x := range exclude {
		if _, ok := tools.All[x]; !ok {
			return fmt.Errorf("-exclude: unknown tool %q", x)
		}
	}
	// Files shared with an included tool (vcruntime140_1.dll) stay.
	var names []string
	for _, t := range tools.All {
		if slices.Contains(exclude, t.Name) {
			continue
		}
		for _, f := range t.Files {
			if _, err := os.Stat(filepath.Join(src, f)); err != nil {
				log.Printf("warning: %s (%s) missing: %v", f, t.Name, err)
				continue
			}
			names = append(names, f)
		}
	}
	if len(names) == 0 {
		// Don't replace a working bundle with an empty one because of a wrong -src.
		return fmt.Errorf("no tool files found in %s", src)
	}
	slices.Sort(names)
	names = slices.Compact(names)

	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	// Without a manifest the bundle is unused, so an interrupted pack can't produce a mismatched one.
	if err := os.Remove(filepath.Join(out, "manifest.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(out, "*.zst"))
	for _, f := range old {
		if _, found := slices.BinarySearch(names, strings.TrimSuffix(filepath.Base(f), ".zst")); !found {
			if err := os.Remove(f); err != nil {
				return err
			}
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
		files  = map[string]tools.ManifestFile{}
		comp   int64
		reused int
		sem    = make(chan struct{}, runtime.NumCPU())
	)
	for _, n := range names {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			mf, csize, kept, err := packFile(filepath.Join(src, n), filepath.Join(out, n+".zst"), level)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", n, err))
				return
			}
			files[n] = mf
			comp += csize
			if kept {
				reused++
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return err
	}

	h := sha256.New()
	for _, n := range slices.Sorted(maps.Keys(files)) {
		fmt.Fprintf(h, "%s\x00%s\n", n, files[n].SHA256)
	}
	m := tools.Manifest{Hash: hex.EncodeToString(h.Sum(nil)[:8]), Files: files}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), b, 0o644); err != nil {
		return err
	}
	var raw int64
	for _, f := range files {
		raw += f.Size
	}
	log.Printf("packed %d files (%d reused), %.1f MB -> %.1f MB, hash %s", len(files), reused, mb(raw), mb(comp), m.Hash)
	return nil
}

// packFile compresses src to dst, unless dst already decompresses to exactly src.
func packFile(src, dst string, level zstd.EncoderLevel) (mf tools.ManifestFile, csize int64, reused bool, err error) {
	if mf, err = digestFile(src); err != nil {
		return mf, 0, false, err
	}
	if have, csize, err := digestZst(dst); err == nil && have == mf {
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
	// Files are already compressed in parallel; one encoder goroutine each keeps memory in check.
	enc, err := zstd.NewWriter(f, zstd.WithEncoderLevel(level), zstd.WithEncoderConcurrency(1))
	if err != nil {
		f.Close()
		return mf, 0, false, err
	}
	_, err = io.Copy(enc, in)
	err = errors.Join(err, enc.Close())
	if fi, serr := f.Stat(); serr == nil {
		csize = fi.Size()
	}
	return mf, csize, false, errors.Join(err, f.Close())
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
