package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"unicode/utf8"
)

func TestParseArgs(t *testing.T) {
	for _, tc := range []struct {
		args, files []string
		level       int
		v           bool
	}{
		{args: nil},
		{args: []string{"a.png"}, files: []string{"a.png"}},
		{args: []string{"-v", "a.png", "b.png"}, files: []string{"a.png", "b.png"}, v: true},
		{args: []string{"a.png", "-level", "3", "b.png", "-v"}, files: []string{"a.png", "b.png"}, level: 3, v: true},
		{args: []string{"a.png", "--", "-v", "-level"}, files: []string{"a.png", "-v", "-level"}},
		{args: []string{"--", "-v"}, files: []string{"-v"}},
		{args: []string{"-level", "4", "--"}, level: 4},
	} {
		fs := flag.NewFlagSet("fox", flag.ContinueOnError)
		level, v := fs.Int("level", 0, ""), fs.Bool("v", false, "")
		files, err := parseArgs(fs, tc.args)
		if err != nil || !slices.Equal(files, tc.files) || *level != tc.level || *v != tc.v {
			t.Errorf("%q: got %q level=%d v=%v err=%v", tc.args, files, *level, *v, err)
		}
	}
	fs := flag.NewFlagSet("fox", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if _, err := parseArgs(fs, []string{"a.png", "-nope"}); err == nil {
		t.Error("unknown flag after a file was accepted")
	}
}

func TestExpand(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.png", "b.png", "c.jpg", "[x].png", filepath.Join("sub", "D.PNG")} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	j := func(n string) string { return filepath.Join(dir, n) }
	for _, tc := range []struct {
		arg  string
		want []string
	}{
		{arg: j("c.jpg"), want: []string{j("c.jpg")}},
		{arg: j("*.png"), want: []string{j("[x].png"), j("a.png"), j("b.png")}},
		{arg: j("?.jpg"), want: []string{j("c.jpg")}},
		{arg: j("[x].png"), want: []string{j("[x].png")}},
		{arg: j(filepath.Join("s*", "*.PNG")), want: []string{j(filepath.Join("sub", "D.PNG"))}},
	} {
		got, err := expand(tc.arg)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %q, %v", tc.arg, got, err)
		}
	}
	upper := j("*.JPG")
	got, err := expand(upper)
	if runtime.GOOS == "windows" && (err != nil || !slices.Equal(got, []string{j("c.jpg")})) {
		t.Errorf("%s: got %q, %v; Windows matching must ignore case", upper, got, err)
	}
	if runtime.GOOS != "windows" && err == nil {
		t.Errorf("%s: got %q", upper, got)
	}
	t.Chdir(dir)
	if got, err := expand("?.jpg"); err != nil || !slices.Equal(got, []string{"c.jpg"}) {
		t.Errorf("relative: got %q, %v", got, err)
	}
}

func TestExpandErrors(t *testing.T) {
	dir := t.TempDir()
	for _, arg := range []string{filepath.Join(dir, "*.gif"), filepath.Join(dir, "missing.png")} {
		got, err := expand(arg)
		if err == nil || got != nil {
			t.Errorf("%s: got %q, %v", arg, got, err)
		}
	}
}

func TestFormat(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.00 KB", 1536: "1.50 KB", 5 << 20: "5.00 MB", 3 << 40: "3.00 TB"} {
		if got := size(n); got != want {
			t.Errorf("size(%d) = %q, want %q", n, got, want)
		}
	}
	if got := percent(1000, 877); got != "-12.3%" {
		t.Errorf("percent = %q", got)
	}
	if got := percent(0, 0); got != "0.0%" {
		t.Errorf("percent of empty = %q", got)
	}
}

func TestFit(t *testing.T) {
	long := `e:\mp3\Isaac Howlett - Eggshell - Ghosts of the Tsunami EP (pre-order)\Isaac Howlett - Eggshell - 03 Eggshell (Radio Edit).mp3`
	for _, tc := range []struct {
		width, used int
		in, want    string
	}{
		{0, 43, long, long},   // not a terminal
		{200, 43, long, long}, // fits
		{50, 43, long, long},  // too narrow to be useful
		{80, 43, "a.png", "a.png"},
		{100, 43, long, `e:\mp3\Isaac Howle…gshell - 03 Eggshell (Radio Edit).mp3`},
	} {
		p := &printer{width: tc.width}
		got := p.fit(tc.in, tc.used)
		if got != tc.want {
			t.Errorf("fit(%d, %d) = %q, want %q", tc.width, tc.used, got, tc.want)
		}
		if tc.width > 0 && got != tc.in && utf8.RuneCountInString(got) != tc.width-tc.used-1 {
			t.Errorf("fit(%d, %d) = %d runes, want %d", tc.width, tc.used, utf8.RuneCountInString(got), tc.width-tc.used-1)
		}
	}
}
