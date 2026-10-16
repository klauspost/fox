package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/klauspost/fox/internal/tools"
)

func readManifest(t *testing.T, out string) tools.Manifest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m tools.Manifest
	if err := json.Unmarshal(b, &m); err != nil || len(m.Hash) != 16 {
		t.Fatalf("manifest %+v, %v", m, err)
	}
	return m
}

func TestPack(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	want := map[string][]byte{}
	for _, f := range tools.All["qpdf"].Files {
		want[f] = bytes.Repeat([]byte(f), 1000)
		os.WriteFile(filepath.Join(src, f), want[f], 0o644)
	}
	os.WriteFile(filepath.Join(out, "stale.exe.zst"), nil, 0o644)

	if err := pack(src, out, zstd.SpeedFastest, nil); err != nil {
		t.Fatal(err)
	}
	m := readManifest(t, out)
	if len(m.Files) != len(want) {
		t.Fatalf("manifest has %d files, want %d", len(m.Files), len(want))
	}
	dec, _ := zstd.NewReader(nil)
	for f, data := range want {
		z, err := os.ReadFile(filepath.Join(out, f+".zst"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := dec.DecodeAll(z, nil)
		if err != nil || !bytes.Equal(got, data) || m.Files[f].Size != int64(len(data)) {
			t.Errorf("%s: roundtrip mismatch (%v)", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "stale.exe.zst")); err == nil {
		t.Error("old .zst not removed")
	}

	// A wrong -src must not wipe the existing bundle.
	if err := pack(t.TempDir(), out, zstd.SpeedFastest, nil); err == nil {
		t.Fatal("packing an empty directory succeeded")
	}
	if _, err := os.Stat(filepath.Join(out, "manifest.json")); err != nil {
		t.Errorf("bundle removed by failed pack: %v", err)
	}
	if err := pack(src, out, zstd.SpeedFastest, []string{"nope"}); err == nil {
		t.Error("unknown -exclude tool accepted")
	}
}

func TestPackIncremental(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	// vcruntime140_1.dll is shared by gs and qpdf, so excluding gs must keep it.
	for _, tool := range []string{"gs", "qpdf", "oxipng"} {
		for _, f := range tools.All[tool].Files {
			os.WriteFile(filepath.Join(src, f), bytes.Repeat([]byte(f), 1000), 0o644)
		}
	}
	zst := func(f string) string { return filepath.Join(out, f+".zst") }
	mtime := func(f string) time.Time {
		fi, err := os.Stat(zst(f))
		if err != nil {
			return time.Time{}
		}
		return fi.ModTime()
	}
	if err := pack(src, out, zstd.SpeedFastest, nil); err != nil {
		t.Fatal(err)
	}
	full := readManifest(t, out)
	old := time.Now().Add(-time.Hour)
	for f := range full.Files {
		os.Chtimes(zst(f), old, old)
	}
	// A corrupt .zst and a changed source must be recompressed.
	os.WriteFile(zst("oxipng.exe"), []byte("garbage"), 0o644)
	os.WriteFile(filepath.Join(src, "qpdf.exe"), []byte("changed"), 0o644)

	if err := pack(src, out, zstd.SpeedFastest, []string{"gs"}); err != nil {
		t.Fatal(err)
	}
	lite := readManifest(t, out)
	if lite.Hash == full.Hash {
		t.Error("lite bundle has the full bundle's hash")
	}
	for _, f := range []string{"gswin64c.exe", "gsdll64.dll"} {
		if _, ok := lite.Files[f]; ok || !mtime(f).IsZero() {
			t.Errorf("%s not excluded", f)
		}
	}
	if !mtime("vcruntime140_1.dll").Equal(old) || !mtime("qpdf30.dll").Equal(old) {
		t.Error("unchanged files were recompressed")
	}
	for _, f := range []string{"qpdf.exe", "oxipng.exe"} {
		if mtime(f).Equal(old) {
			t.Errorf("%s: stale .zst reused", f)
		}
	}
	dec, _ := zstd.NewReader(nil)
	z, _ := os.ReadFile(zst("qpdf.exe"))
	if got, err := dec.DecodeAll(z, nil); err != nil || string(got) != "changed" || lite.Files["qpdf.exe"].Size != 7 {
		t.Errorf("qpdf.exe: got %q, %v", got, err)
	}

	os.WriteFile(filepath.Join(src, "qpdf.exe"), bytes.Repeat([]byte("qpdf.exe"), 1000), 0o644)
	if err := pack(src, out, zstd.SpeedFastest, nil); err != nil {
		t.Fatal(err)
	}
	if again := readManifest(t, out); again.Hash != full.Hash {
		t.Errorf("full bundle hash changed: %s != %s", again.Hash, full.Hash)
	}
}
