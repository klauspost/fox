package pipeline

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/klauspost/fox/internal/config"
	"github.com/klauspost/fox/internal/detect"
)

// The Func steps are exercised against the bundled Windows tools when the reference tree is present.
const plugins = "../../_ref/Win64/Debug/Plugins64"

func realExec(t *testing.T) *Exec {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("bundled tools are Windows binaries")
	}
	dir, err := filepath.Abs(plugins)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skip("reference tools not present")
	}
	scratch := t.TempDir()
	cmd := func(ctx context.Context, tool string, args []string) *exec.Cmd {
		c := exec.CommandContext(ctx, filepath.Join(dir, tool+".exe"), args...)
		c.Dir = scratch
		return c
	}
	return &Exec{
		Dir: scratch,
		Run: func(ctx context.Context, tool string, args ...string) error {
			if out, err := cmd(ctx, tool, args).CombinedOutput(); err != nil {
				t.Logf("%s: %v: %s", tool, err, out)
				return err
			}
			return nil
		},
		RunStdio: func(ctx context.Context, tool, stdin, stdout string, args ...string) error {
			c := cmd(ctx, tool, args)
			if stdin != "" {
				f, err := os.Open(stdin)
				if err != nil {
					return err
				}
				defer f.Close()
				c.Stdin = f
			}
			f, err := os.Create(stdout)
			if err != nil {
				return err
			}
			defer f.Close()
			c.Stdout = f
			return c.Run()
		},
	}
}

func size(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func TestLibdeflate(t *testing.T) {
	x := realExec(t)
	dir := t.TempDir()
	raw, in, out := filepath.Join(dir, "raw.txt"), filepath.Join(dir, "in.gz"), filepath.Join(dir, "out.gz")
	data := bytes.Repeat([]byte("fox optimizes files concurrently. "), 500)
	if err := os.WriteFile(raw, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := x.RunStdio(t.Context(), "gzip", raw, in, "-c", "-1"); err != nil {
		t.Fatal(err)
	}
	if err := libdeflate(t.Context(), x, in, out); err != nil {
		t.Fatal(err)
	}
	back := filepath.Join(dir, "back")
	if err := x.RunStdio(t.Context(), "gzip", out, back, "-c", "-d"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(back); !bytes.Equal(got, data) {
		t.Fatal("round trip mismatch")
	}
}

func TestSQLiteVacuum(t *testing.T) {
	x := realExec(t)
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.db"), filepath.Join(dir, "out.db")
	if err := x.Run(t.Context(), "sqlite3", in, "PRAGMA journal_mode=WAL; PRAGMA user_version=7; CREATE TABLE a(x); "+
		"INSERT INTO a SELECT randomblob(2000) FROM (SELECT 1 UNION SELECT 2 UNION SELECT 3); DELETE FROM a WHERE rowid>1;"); err != nil {
		t.Fatal(err)
	}
	if err := sqliteVacuum(t.Context(), x, in, out); err != nil {
		t.Fatal(err)
	}
	if size(t, out) >= size(t, in) {
		t.Errorf("not smaller: %d >= %d", size(t, out), size(t, in))
	}
	if !walMode(out) {
		t.Error("WAL mode lost")
	}
	res := filepath.Join(dir, "res.txt")
	if err := x.RunStdio(t.Context(), "sqlite3", "", res, out, "PRAGMA user_version; SELECT count(*) FROM a;"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(res); strings.Fields(string(got))[0] != "7" || strings.Fields(string(got))[1] != "1" {
		t.Errorf("got %q", got)
	}
}

// wavFile returns 16 bit mono PCM: half a second of silence, then a tone, plus a LIST chunk.
func wavFile() []byte {
	const rate = 8000
	pcm := make([]byte, rate*2)
	for i := rate / 2; i < rate; i++ {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(int16(8000*(i%16/8*2-1))))
	}
	fmtc := binary.LittleEndian.AppendUint16(nil, 1)
	fmtc = binary.LittleEndian.AppendUint16(fmtc, 1)
	fmtc = binary.LittleEndian.AppendUint32(fmtc, rate)
	fmtc = binary.LittleEndian.AppendUint32(fmtc, rate*2)
	fmtc = binary.LittleEndian.AppendUint16(fmtc, 2)
	fmtc = binary.LittleEndian.AppendUint16(fmtc, 16)
	chunk := func(id string, d []byte) []byte {
		return append(binary.LittleEndian.AppendUint32([]byte(id), uint32(len(d))), d...)
	}
	body := append([]byte("WAVE"), chunk("fmt ", fmtc)...)
	body = append(body, chunk("LIST", []byte("INFOINAM\x04\x00\x00\x00fox\x00"))...)
	body = append(body, chunk("data", pcm)...)
	return chunk("RIFF", body)
}

func TestShntool(t *testing.T) {
	x := realExec(t)
	dir := t.TempDir()
	in := filepath.Join(dir, "in.wav")
	if err := os.WriteFile(in, wavFile(), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"strip", "trim"} {
		out := filepath.Join(dir, mode+".wav")
		if err := shntoolFunc(mode)(t.Context(), x, in, out); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if size(t, out) >= size(t, in) {
			t.Errorf("%s: not smaller: %d >= %d", mode, size(t, out), size(t, in))
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 3 {
		t.Errorf("shntool left files behind: %v", entries)
	}
}

func TestFLACTrim(t *testing.T) {
	x := realExec(t)
	dir := t.TempDir()
	wav, in, out := filepath.Join(dir, "a.wav"), filepath.Join(dir, "in.flac"), filepath.Join(dir, "out.flac")
	if err := os.WriteFile(wav, wavFile(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := x.Run(t.Context(), "flac", "-s", "-f", "-o", in, wav); err != nil {
		t.Fatal(err)
	}
	if err := flacTrim(flacFlags(5))(t.Context(), x, in, out); err != nil {
		t.Fatal(err)
	}
	if size(t, out) >= size(t, in) {
		t.Errorf("not smaller: %d >= %d", size(t, out), size(t, in))
	}
}

func TestM7zRepack(t *testing.T) {
	if testing.Short() {
		t.Skip("m7zRepacker takes a while")
	}
	x := realExec(t)
	dir := t.TempDir()
	src, in, out := filepath.Join(dir, "a.txt"), filepath.Join(dir, "in.7z"), filepath.Join(dir, "out.7z")
	if err := os.WriteFile(src, bytes.Repeat([]byte("fox repacks 7z archives. "), 4000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := x.Run(t.Context(), "7z", "a", "-mx1", in, src); err != nil {
		t.Fatal(err)
	}
	if err := m7zRepack(t.Context(), x, in, out); err != nil {
		t.Fatal(err)
	}
	if size(t, out) >= size(t, in) {
		t.Errorf("not smaller: %d >= %d", size(t, out), size(t, in))
	}
	if err := x.Run(t.Context(), "7z", "t", out); err != nil {
		t.Errorf("repacked archive fails 7z t: %v", err)
	}
}

func TestSameAudio(t *testing.T) {
	x := realExec(t)
	dir := t.TempDir()
	for _, tt := range []struct {
		name, duration string
		enc            []string
		ok             bool
	}{
		// ffmpeg's LAME tag has a CRC mp3packer rejects, so it zeroes the encoder delay and padding.
		{"lame tag.mp3", "2", []string{"-c:a", "libmp3lame"}, false},
		{"no tag.mp3", "2", []string{"-c:a", "libmp3lame", "-write_xing", "0"}, true},
		// optivorbis puts all audio in one page, and ffmpeg then decodes 128 samples less.
		{"short.ogg", "2", []string{"-c:a", "libvorbis"}, false},
		{"long.ogg", "20", []string{"-c:a", "libvorbis"}, true},
	} {
		in, out := filepath.Join(dir, tt.name), filepath.Join(dir, "out-"+tt.name)
		args := append([]string{"-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=" + tt.duration}, tt.enc...)
		if err := x.Run(t.Context(), "ffmpeg", append(args, in)...); err != nil {
			t.Fatal(err)
		}
		f := map[string]detect.Format{".mp3": detect.MP3, ".ogg": detect.OGG}[filepath.Ext(in)]
		steps, _ := For(f, in, config.Default())
		if err := steps[0].Func(t.Context(), x, in, out); (err == nil) != tt.ok {
			t.Errorf("%s: got error %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
	// ffmpeg reports non-monotonic timestamps at each link of a chained Ogg, which must not fail the check.
	b := filepath.Join(dir, "b.ogg")
	if err := x.Run(t.Context(), "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=880:duration=20", "-c:a", "libvorbis", b); err != nil {
		t.Fatal(err)
	}
	long, _ := os.ReadFile(filepath.Join(dir, "long.ogg"))
	second, _ := os.ReadFile(b)
	in := filepath.Join(dir, "chain.ogg")
	if err := os.WriteFile(in, append(long, second...), 0o644); err != nil {
		t.Fatal(err)
	}
	steps, _ := For(detect.OGG, in, config.Default())
	if err := steps[0].Func(t.Context(), x, in, filepath.Join(dir, "out-chain.ogg")); err != nil {
		t.Errorf("chained: %v", err)
	}
}
