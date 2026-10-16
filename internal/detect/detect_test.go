package detect

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pngSig = "\x89PNG\r\n\x1a\n"

func chunk(typ string, data string) string {
	return string(binary.BigEndian.AppendUint32(nil, uint32(len(data)))) + typ + data + "CRC!"
}

func jpeg(nf byte, prefix string) string {
	sof := "\xff\xc2\x00\x0b\x08\x00\x10\x00\x10" + string(nf) + "\x01\x11\x00"
	return "\xff\xd8" + prefix + "\xff\xe0\x00\x10JFIF\x00\x01\x02\x00\x00\x01\x00\x01\x00\x00" + sof + "\xff\xda\x00\x08"
}

func tiff(bo interface {
	binary.ByteOrder
	binary.AppendByteOrder
}, photometric uint16) string {
	b := []byte("II*\x00")
	if bo == binary.BigEndian {
		b = []byte("MM\x00*")
	}
	b = bo.AppendUint32(b, 8)
	b = bo.AppendUint16(b, 2)
	b = append(bo.AppendUint16(bo.AppendUint16(b, 256), 3), 0, 0, 0, 1, 0, 16, 0, 0)
	b = bo.AppendUint16(bo.AppendUint16(b, 262), 3)
	b = bo.AppendUint32(b, 1)
	b = append(bo.AppendUint16(b, photometric), 0, 0)
	return string(b)
}

// peFile is a minimal PE header the sniffer accepts.
func peFile() string {
	b := make([]byte, 0x80)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[60:], 0x40)
	copy(b[0x40:], "PE\x00\x00")
	return string(b)
}

func write(t testing.TB, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetect(t *testing.T) {
	tarHdr := "BMfake.txt" + strings.Repeat("\x00", 247) + "ustar\x0000" + strings.Repeat("\x00", 243)
	riff := func(kind string) string { return "RIFF\x24\x00\x00\x00" + kind + "fmt " }
	ftyp := func(brand string) string { return "\x00\x00\x00\x18ftyp" + brand + "\x00\x00\x00\x00isom" }
	png := pngSig + chunk("IHDR", strings.Repeat("\x00", 13))
	bmp := "BM\x46\x00\x00\x00\x00\x00\x00\x00\x36\x00\x00\x00\x28\x00\x00\x00" + strings.Repeat("\x00", 32)

	tests := []struct {
		name, content string
		jsExt         []string
		want          Format
	}{
		{"a.png", png, nil, PNG},
		{"a.PNG", png, nil, PNG},
		{"renamed.png", jpeg(3, ""), nil, JPEG},
		{"renamed.jpg", png, nil, PNG},
		{"noext", png, nil, PNG},
		{"a.jfif", jpeg(3, ""), nil, JPEG},
		{"truncated.png", "\x89P", nil, ""},
		{"empty.png", "", nil, ""},
		{"empty.mp4", "", nil, ""},
		{"text.png", "not an image", nil, ""},
		{"Thumbs.db", "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1", nil, ""},
		{"zip.flac", "PK\x03\x04", nil, ZIP},
		{"empty.bin", "", nil, ""},
		{"a.gif", "GIF89a", nil, GIF},
		{"a.webp", riff("WEBP"), nil, WebP},
		{"a.wav", riff("WAVE"), nil, WAV},
		{"webp.wav", riff("WEBP"), nil, WebP},
		{"wav.bin", riff("WAVE"), nil, WAV},
		{"avi.bin", riff("AVI "), nil, ""},
		{"a.bmp", bmp, nil, BMP},
		{"bm.txt", "BMW is a car brand, not a bitmap", nil, ""},
		{"a.ico", "\x00\x00\x01\x00\x01\x00", nil, ICO},
		{"a.cur", "\x00\x00\x02\x00\x01\x00", nil, ICO},
		{"pcm.raw", "\x00\x00\x01\x00\x01\x00", nil, ""},
		{"ico.png", "\x00\x00\x01\x00\x01\x00", nil, ""},
		{"le.tif", tiff(binary.LittleEndian, 2), nil, TIFF},
		{"raw.cr2", tiff(binary.LittleEndian, 2), nil, ""},
		{"tiff.jpg", tiff(binary.LittleEndian, 2), nil, ""},
		{"be.tiff", tiff(binary.BigEndian, 2), nil, TIFF},
		{"raw.dng", tiff(binary.BigEndian, 2), nil, ""},
		{"a.pdf", "%PDF-1.7\n", nil, PDF},
		{"a.ai", "%PDF-1.5\n", nil, PDF},
		{"ps.ai", "%!PS-Adobe-3.0\n", nil, ""},
		{"junk.pdf", "\x00\x05\x16\x07junk%PDF-1.4\n", nil, PDF},
		{"junk.bin", "\x00\x05\x16\x07junk%PDF-1.4\n", nil, ""},
		{"a.zip", "PK\x03\x04", nil, ZIP},
		{"a.docx", "PK\x03\x04", nil, ZIP},
		{"zip.unknown", "PK\x03\x04", nil, ZIP},
		{"a.apk", "PK\x03\x04", nil, ""},
		{"a.APPX", "PK\x03\x04", nil, ""},
		{"a.ipa", "PK\x03\x04", nil, ""},
		{"a.svgz", "\x1f\x8b\x08\x00", nil, GZ},
		{"a.tgz", "\x1f\x8b\x08\x00", nil, GZ},
		{"a.tar", tarHdr, nil, TAR},
		{"tar.bin", tarHdr, nil, TAR},
		{"short.bin", tarHdr[:200], nil, ""},
		{"a.7z", "7z\xbc\xaf\x27\x1c\x00\x04", nil, SevenZ},
		{"a.db", "SQLite format 3\x00", nil, SQLite},
		{"sqlite.bin", "SQLite format 3\x00", nil, SQLite},
		{"a.flac", "fLaC", nil, FLAC},
		{"id3.flac", "ID3\x04\x00\x00\x00\x00\x00\x00fLaC", nil, FLAC},
		{"id3.mp3", "ID3\x04\x00\x00\x00\x00\x00\x00", nil, MP3},
		{"id3.aac", "ID3\x04\x00\x00\x00\x00\x00\x00", nil, ""},
		{"id3.wav", "ID3\x04\x00\x00\x00\x00\x00\x00", nil, ""},
		{"sync.mp3", "\xff\xfb\x90\x00", nil, MP3},
		{"sync.bin", "\xff\xfb\x90\x00", nil, ""},
		{"a.opus", "OggS", nil, OGG},
		{"a.mp4", ftyp("isom"), nil, MP4},
		{"a.m4a", ftyp("M4A "), nil, MP4},
		{"a.mov", "\x00\x00\x00\x08wide", nil, MP4},
		{"isom.bin", ftyp("isom"), nil, ""},
		{"raw.cr3", ftyp("crx "), nil, ""},
		{"mp4.mp3", ftyp("isom"), nil, ""},
		{"avif.mp4", ftyp("avif"), nil, ""},
		{"a.heic", ftyp("heic"), nil, ""},
		{"a.jxl", ftyp("jxl "), nil, ""},
		{"ebml.bin", "\x1a\x45\xdf\xa3", nil, MKV},
		{"a.webm", "\x1a\x45\xdf\xa3", nil, MKV},
		{"a.exe", peFile(), nil, EXE},
		{"a.dll", peFile(), nil, DLL},
		{"a.ocx", peFile(), nil, DLL},
		{"pe.bin", peFile(), nil, ""},
		{"driver.sys", peFile(), nil, ""},
		{"exe.png", peFile(), nil, ""},
		{"text.exe", "plain text", nil, ""},
		{"mz.txt", "MZ is not enough", nil, ""},
		{"a.svg", pngSig, nil, SVG},
		{"a.html", "<html>", nil, HTML},
		{"a.css", "a{}", nil, CSS},
		{"a.mjs", "x", nil, JS},
		{"a.json", "{}", nil, JS},
		{"a.gpx", "<gpx/>", nil, ""},
		{"a.xml", "<?xml version=\"1.0\"?><a/>", nil, ""},
		{"html.txt", "<html>", nil, ""},
		{"a.ts", "x", []string{"ts"}, JS},
		{"a.TS", "x", []string{".ts"}, JS},
		{"a.ts", "x", nil, ""},
		{"a.pcx", "\x0a\x05\x01\x08", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Detect(write(t, tt.name, tt.content), tt.jsExt)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectMissing(t *testing.T) {
	if _, err := Detect(filepath.Join(t.TempDir(), "missing.png"), nil); err == nil {
		t.Error("want error for missing binary file")
	}
}

func TestIsAPNG(t *testing.T) {
	ihdr := chunk("IHDR", strings.Repeat("\x00", 13))
	idat := chunk("IDAT", "data")
	tests := []struct {
		name, content string
		want          bool
	}{
		{"apng", pngSig + ihdr + chunk("acTL", "\x00\x00\x00\x02\x00\x00\x00\x00") + idat, true},
		{"fcTL", pngSig + ihdr + chunk("fcTL", strings.Repeat("\x00", 26)) + idat, true},
		{"static", pngSig + ihdr + idat + chunk("IEND", ""), false},
		{"after IDAT", pngSig + ihdr + idat + chunk("acTL", ""), false},
		{"truncated", pngSig + ihdr[:6], false},
		{"huge length", pngSig + "\xff\xff\xff\xffIHDR", false},
		{"not png", "GIF89a" + chunk("acTL", ""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAPNG(write(t, "a.png", tt.content)); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsJPEGCMYK(t *testing.T) {
	// An EXIF segment containing bytes that look like a 4-component SOF must not be mistaken for the frame header.
	fakeSOF := "\xff\xc0\x00\x0b\x08\x00\x10\x00\x10\x04"
	exif := "\xff\xe1" + string(binary.BigEndian.AppendUint16(nil, uint16(2+len(fakeSOF)))) + fakeSOF
	tests := []struct {
		name, content string
		want          bool
	}{
		{"cmyk", jpeg(4, ""), true},
		{"rgb", jpeg(3, ""), false},
		{"exif decoy", jpeg(3, exif), false},
		{"fill bytes", "\xff\xd8\xff\xff" + jpeg(4, "")[2:], true},
		{"truncated sof", jpeg(4, "")[:24], false},
		{"no sof", "\xff\xd8\xff\xda\x00\x08", false},
		{"garbage", "\xff\xd8\x00\x00\x00\x00", false},
		{"png", pngSig, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsJPEGCMYK(write(t, "a.jpg", tt.content)); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsNinePatch(t *testing.T) {
	for p, want := range map[string]bool{
		`C:\res\button.9.png`: true,
		"res/BUTTON.9.PNG":    true,
		"button.png":          false,
		"button.9.jpg":        false,
	} {
		if got := IsNinePatch(p); got != want {
			t.Errorf("IsNinePatch(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestIsPDFLayered(t *testing.T) {
	if !IsPDFLayered(write(t, "a.pdf", "%PDF-1.5\n1 0 obj << /Type /OCG /Name (Layer) >> endobj")) {
		t.Error("layered PDF not detected")
	}
	if IsPDFLayered(write(t, "b.pdf", "%PDF-1.5\n1 0 obj << /Type /Page >> endobj")) {
		t.Error("plain PDF detected as layered")
	}
	if IsPDFLayered(filepath.Join(t.TempDir(), "missing.pdf")) {
		t.Error("missing file detected as layered")
	}
}

func TestIsSFXSynthetic(t *testing.T) {
	for content, want := range map[string]bool{
		peFile() + "Inno Setup Setup Data":      true,
		peFile() + "Nullsoft Install System v3": true,
		peFile() + "nothing here":               false,
		"Inno Setup without MZ":                 false,
		"":                                      false,
	} {
		if got := IsSFX(write(t, "a.exe", content)); got != want {
			t.Errorf("IsSFX(%q) = %v, want %v", content[min(len(content), 0x80):], got, want)
		}
	}
}

func plugin(t *testing.T, name string) string {
	p := filepath.Join("..", "..", "_ref", "Win64", "Debug", "Plugins64", name)
	if _, err := os.Stat(p); err != nil {
		t.Skip("bundled tools not available:", err)
	}
	return p
}

func TestIsSFXReal(t *testing.T) {
	// Leanify embeds the zip signature as a constant; only data appended after the PE image counts.
	exe := plugin(t, "Leanify.exe")
	if IsSFX(exe) {
		t.Errorf("%s detected as SFX", exe)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("payload.txt")
	io.WriteString(w, "hello")
	zw.Close()
	if sfx := write(t, "sfx.exe", string(b)+buf.String()); !IsSFX(sfx) {
		t.Error("exe with appended zip not detected as SFX")
	}
}

func TestIsSigned(t *testing.T) {
	if IsSigned(plugin(t, "jhead.exe")) {
		t.Error("jhead.exe reported as signed")
	}
	if IsSigned(write(t, "a.exe", peFile())) {
		t.Error("synthetic PE reported as signed")
	}
	// These carry embedded Authenticode signatures; many other system binaries are only catalog-signed.
	for _, p := range []string{`C:\Windows\explorer.exe`, `C:\Windows\System32\ntoskrnl.exe`} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if !IsSigned(p) {
			t.Errorf("%s not reported as signed", p)
		}
		return
	}
	t.Skip("no embedded-signed executable found")
}

func FuzzHelpers(f *testing.F) {
	f.Add([]byte(jpeg(4, "")))
	f.Add([]byte(tiff(binary.BigEndian, 5)))
	f.Add([]byte(pngSig + chunk("IHDR", "") + chunk("acTL", "")))
	f.Add([]byte(peFile() + "PK\x03\x04"))
	f.Add([]byte("\x00\x00\x00\x18ftypavif"))
	f.Add([]byte(jpeg(3, exif(binary.LittleEndian, 0x0112, 6))))
	f.Add([]byte(pngSig + chunk("IHDR", "") + pngChunk("eXIf", exifTIFF(binary.BigEndian, 0x0112, 6))))
	f.Add([]byte("RIFF\x00\x00\x00\x00WEBP" + riffChunk("EXIF", exifTIFF(binary.LittleEndian, 0x0112, 6))))
	f.Add([]byte(oggPage(true, "\x01vorbis") + oggPage(false, "data")))
	f.Add([]byte(ebml(0x1a45dfa3) + ebml(0x18538067, ebml(0x1654ae6b, ebml(0xae, ebml(0xe0, ebml(0x55b0)))))))
	f.Add([]byte(box("ftyp", "isom") + box("moov", box("mvex"))))
	f.Add([]byte(jpeg(3, app("\xe2", "ICC_PROFILE\x00\x01\x01"+iccProfile(adobeD50, gamma22)))))
	f.Add([]byte(pngSig + chunk("IHDR", "") + pngChunk("iCCP", "p\x00\x00"+zlibString(iccProfile(srgbD50, srgbCurv))) + chunk("gAMA", "\x00\x00\xb1\x8f")))
	f.Add([]byte(webpFile(riffChunk("VP8X", "\x22\x00\x00\x00\x00\x00\x00\x00\x00\x00"), riffChunk("ICCP", iccProfile(p3D50, srgbPara)), riffChunk("ANIM", ""))))
	f.Add([]byte(ico(pngSig + pngChunk("iCCP", "p\x00\x00"+zlibString(iccProfile(srgbD50, srgbPara))) + pngChunk("IEND", ""))))
	f.Add([]byte(webpFile(riffChunk("ICCP", iccProfile(srgbD50, srgbCurv, "A2B0")))))
	f.Add([]byte(jpeg(3, app("\xe1", "http://ns.adobe.com/xap/1.0/\x00hdrgm")) + "\xff\xd9" + jpeg(3, "")))
	f.Add([]byte(tiffICC(binary.BigEndian, iccProfile(adobeD50, gamma22))))
	f.Add([]byte(bmpV5("DEBM", iccProfile(srgbD50, srgbCurv))))
	f.Add([]byte(gifICC(iccProfile(p3D50, srgbPara))))
	f.Fuzz(func(t *testing.T, b []byte) {
		sniff(b)
		p := write(t, "f.bin", string(b))
		Detect(p, nil)
		IsAPNG(p)
		IsJPEGCMYK(p)
		IsPDFLayered(p)
		IsSFX(p)
		IsSigned(p)
		HasPEOverlay(p)
		Orientation(p)
		ColorManaged(p)
		IsAnimatedWebP(p)
		HasGainMap(p)
		HasZIPDirs(p)
		TIFFTags(p)
		IsPDFSigned(p)
		IsPDFA(p)
		IsFragmentedMP4(p)
		OggCodecs(p)
		MatroskaV4(p)
		HasZIPUnicodeExtra(p)
	})
}
