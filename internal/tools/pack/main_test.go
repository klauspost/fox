package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/klauspost/fox/internal/tools"
)

func readManifest(t *testing.T, dir string) tools.Manifest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m tools.Manifest
	if err := json.Unmarshal(b, &m); err != nil || len(m.Hash) != 16 {
		t.Fatalf("manifest %+v, %v", m, err)
	}
	return m
}

func writeSources(t *testing.T, src string, names ...string) {
	t.Helper()
	for _, name := range names {
		for _, f := range tools.All[name].Files {
			if err := os.WriteFile(filepath.Join(src, f), bytes.Repeat([]byte(f), 1000), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestPack(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	// vcruntime140_1.dll is shared by gs (extra) and qpdf (core), so it must go to core.
	writeSources(t, src, "qpdf", "gs", "pingo")
	want := map[string][]string{
		"core":  {"qpdf.exe", "qpdf30.dll", "vcruntime140_1.dll"},
		"extra": {"gswin64c.exe", "gsdll64.dll"},
	}
	for _, g := range groups {
		os.MkdirAll(filepath.Join(out, g), 0o755)
	}
	os.WriteFile(filepath.Join(out, "core", "stale.exe.zst"), nil, 0o644)
	os.WriteFile(filepath.Join(out, "extra", "qpdf.exe.zst"), nil, 0o644) // wrong group
	os.WriteFile(filepath.Join(out, "core", "pingo.exe.zst"), nil, 0o644) // host-only

	if err := pack(src, out, zstd.SpeedFastest); err != nil {
		t.Fatal(err)
	}
	dec, _ := zstd.NewReader(nil)
	for g, names := range want {
		m := readManifest(t, filepath.Join(out, g))
		if len(m.Files) != len(names) {
			t.Errorf("%s: manifest has %d files, want %d", g, len(m.Files), len(names))
		}
		for _, f := range names {
			z, err := os.ReadFile(filepath.Join(out, g, f+".zst"))
			if err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(filepath.Join(src, f))
			got, err := dec.DecodeAll(z, nil)
			if err != nil || !bytes.Equal(got, data) || m.Files[f].Size != int64(len(data)) {
				t.Errorf("%s/%s: roundtrip mismatch (%v)", g, f, err)
			}
		}
	}
	for _, f := range []string{"core/stale.exe.zst", "extra/qpdf.exe.zst", "core/pingo.exe.zst"} {
		if _, err := os.Stat(filepath.Join(out, f)); err == nil {
			t.Errorf("%s not removed", f)
		}
	}

	// A wrong -src must not wipe the existing bundle.
	if err := pack(t.TempDir(), out, zstd.SpeedFastest); err == nil {
		t.Fatal("packing an empty directory succeeded")
	}
	if _, err := os.Stat(filepath.Join(out, "core", "manifest.json")); err != nil {
		t.Errorf("bundle removed by failed pack: %v", err)
	}
}

func TestPackIncremental(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	writeSources(t, src, "gs", "qpdf", "oxipng")
	zst := func(f string) string { return filepath.Join(out, "core", f+".zst") }
	mtime := func(f string) time.Time {
		fi, err := os.Stat(zst(f))
		if err != nil {
			return time.Time{}
		}
		return fi.ModTime()
	}
	if err := pack(src, out, zstd.SpeedFastest); err != nil {
		t.Fatal(err)
	}
	core, extra := readManifest(t, filepath.Join(out, "core")), readManifest(t, filepath.Join(out, "extra"))
	old := time.Now().Add(-time.Hour)
	for f := range core.Files {
		os.Chtimes(zst(f), old, old)
	}
	// A file whose group changed is moved, not recompressed.
	if err := os.Rename(zst("vcruntime140_1.dll"), filepath.Join(out, "extra", "vcruntime140_1.dll.zst")); err != nil {
		t.Fatal(err)
	}
	// A corrupt .zst and a changed source must be recompressed.
	os.WriteFile(zst("oxipng.exe"), []byte("garbage"), 0o644)
	os.WriteFile(filepath.Join(src, "qpdf.exe"), []byte("changed"), 0o644)

	if err := pack(src, out, zstd.SpeedFastest); err != nil {
		t.Fatal(err)
	}
	changed := readManifest(t, filepath.Join(out, "core"))
	if changed.Hash == core.Hash {
		t.Error("core hash unchanged after a file changed")
	}
	if again := readManifest(t, filepath.Join(out, "extra")); again.Hash != extra.Hash {
		t.Error("extra hash changed")
	}
	if !mtime("vcruntime140_1.dll").Equal(old) || !mtime("qpdf30.dll").Equal(old) {
		t.Error("unchanged or moved files were recompressed")
	}
	for _, f := range []string{"qpdf.exe", "oxipng.exe"} {
		if mtime(f).Equal(old) {
			t.Errorf("%s: stale .zst reused", f)
		}
	}
	dec, _ := zstd.NewReader(nil)
	z, _ := os.ReadFile(zst("qpdf.exe"))
	if got, err := dec.DecodeAll(z, nil); err != nil || string(got) != "changed" || changed.Files["qpdf.exe"].Size != 7 {
		t.Errorf("qpdf.exe: got %q, %v", got, err)
	}

	os.WriteFile(filepath.Join(src, "qpdf.exe"), bytes.Repeat([]byte("qpdf.exe"), 1000), 0o644)
	if err := pack(src, out, zstd.SpeedFastest); err != nil {
		t.Fatal(err)
	}
	if again := readManifest(t, filepath.Join(out, "core")); again.Hash != core.Hash {
		t.Errorf("core hash changed: %s != %s", again.Hash, core.Hash)
	}
}

func TestPackLimit(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	noise := make([]byte, 10000)
	for i := range noise {
		noise[i] = byte(rand.Uint32())
	}
	os.WriteFile(filepath.Join(src, "oxipng.exe"), noise, 0o644)
	if err := pack(src, out, zstd.SpeedFastest); err != nil {
		t.Fatal(err)
	}
	old := maxZst
	maxZst = 5000
	t.Cleanup(func() { maxZst = old })
	// Also an existing .zst that is now too large must not be kept.
	err := pack(src, out, zstd.SpeedFastest)
	if err == nil || !strings.Contains(err.Error(), "100 MB") {
		t.Fatalf("got %v, want size error", err)
	}
	for _, f := range []string{"oxipng.exe.zst", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(out, "core", f)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind: %v", f, err)
		}
	}
}
