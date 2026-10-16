// Package detect identifies file formats by extension and content.
package detect

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"debug/pe"
	"encoding/binary"
	"io"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Format is a pipeline identifier. The empty Format means unsupported.
type Format string

const (
	JPEG   Format = "jpeg"
	PNG    Format = "png"
	ICO    Format = "ico"
	GIF    Format = "gif"
	WebP   Format = "webp"
	BMP    Format = "bmp"
	TIFF   Format = "tiff"
	SVG    Format = "svg"
	PDF    Format = "pdf"
	HTML   Format = "html"
	CSS    Format = "css"
	JS     Format = "js"
	ZIP    Format = "zip"
	GZ     Format = "gz"
	TAR    Format = "tar"
	SevenZ Format = "7z"
	SQLite Format = "sqlite"
	FLAC   Format = "flac"
	WAV    Format = "wav"
	MP3    Format = "mp3"
	OGG    Format = "ogg"
	MP4    Format = "mp4"
	MKV    Format = "mkv"
	EXE    Format = "exe"
	DLL    Format = "dll"
)

// unsupported is a sniff result for content we recognize but must not touch,
// so it can override a misleading extension.
const unsupported Format = "-"

// Signed packages: any rewrite of the container invalidates their signature.
var deny = set(".apk .aab .ipa .appx .appxbundle .msix .msixbundle .xpi .crx .ipsw .vsix .nupkg")

var byExt = func() map[string]Format {
	m := map[string]Format{}
	for f, exts := range map[Format]string{
		JPEG:   ".jpg .jpeg .jpe .jfif .jfi .jif",
		PNG:    ".png .apng",
		ICO:    ".ico .cur",
		GIF:    ".gif",
		WebP:   ".webp",
		BMP:    ".bmp .dib",
		TIFF:   ".tif .tiff",
		SVG:    ".svg",
		PDF:    ".pdf .ai",
		HTML:   ".html .htm",
		CSS:    ".css",
		JS:     ".js .mjs .cjs .json",
		ZIP:    ".zip .zipx .docx .docm .dotx .dotm .xlsx .xlsm .xlsb .xltx .xltm .pptx .pptm .potx .potm .ppsx .ppsm .odt .ods .odp .odg .odf .odb .epub .cbz .jar .war .ear .kmz .xps .oxps .ora .vsdx .3mf",
		GZ:     ".gz .tgz .svgz",
		TAR:    ".tar",
		SevenZ: ".7z",
		SQLite: ".sqlite .sqlite3 .db",
		FLAC:   ".flac",
		WAV:    ".wav",
		MP3:    ".mp3",
		OGG:    ".ogg .oga .ogv .opus .spx",
		MP4:    ".mp4 .m4a .m4v .m4b .mov .3gp .3g2",
		MKV:    ".mkv .mka .mk3d .webm",
		EXE:    ".exe",
		DLL:    ".dll .ocx .cpl",
	} {
		for e := range set(exts) {
			m[e] = f
		}
	}
	return m
}()

func set(s string) map[string]bool {
	m := map[string]bool{}
	for e := range strings.FieldsSeq(s) {
		m[e] = true
	}
	return m
}

func isText(f Format) bool {
	switch f {
	case SVG, HTML, CSS, JS:
		return true
	}
	return false
}

// HEIF/AVIF/JPEG XL share the ISO-BMFF container with MP4, but remuxing them as video would break them.
var stillBrands = []string{"avif", "avis", "heic", "heix", "heim", "heis", "mif1", "msf1", "jxl "}

// These headers are short or shared with files we must not touch (raw PCM, AAC,
// camera raw, HEIF, drivers, EFI kernels), so they only confirm a matching extension.
var confirmOnly = []Format{ICO, MP3, MP4, TIFF, EXE, DLL}

// Known reports whether the file's extension maps to a supported format.
// Content sniffing of unknown extensions is reserved for files the user named explicitly,
// since directories can hold content-addressed blobs (.git/lfs, caches) that must not change.
func Known(path string, jsExt []string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return byExt[ext] != "" || ext != "" && slices.ContainsFunc(jsExt, func(e string) bool { return strings.EqualFold("."+strings.TrimPrefix(e, "."), ext) })
}

// Detect returns the format of the file at path, or "" if unsupported, empty,
// or its content contradicts its extension.
// jsExt lists extra extensions (".mjs") to be treated as JavaScript.
func Detect(path string, jsExt []string) (Format, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if deny[ext] {
		return "", nil
	}
	f := byExt[ext]
	if ext != "" && slices.ContainsFunc(jsExt, func(e string) bool { return strings.EqualFold("."+strings.TrimPrefix(e, "."), ext) }) {
		f = JS
	}
	if isText(f) {
		return f, nil
	}
	head, err := readAt(path, 0, 1024)
	if err != nil || len(head) == 0 {
		return "", err
	}
	s := sniff(head)
	if s == EXE && ext != ".exe" {
		s = DLL
	}
	switch {
	case s == unsupported:
		return "", nil
	case s == f, s == MP3 && f == FLAC:
		return f, nil
	case s != "" && !slices.Contains(confirmOnly, s):
		return s, nil
	// Signature is optional (MP3 without ID3, old QuickTime, v7 tar) or may follow junk (PDF).
	case s == "" && (f == MP3 || f == MP4 || f == TAR || f == PDF && bytes.Contains(head, []byte("%PDF-"))):
		return f, nil
	}
	// The content contradicts the extension (Thumbs.db, PostScript .ai, ...):
	// tools may mangle such a file instead of failing.
	return "", nil
}

func sniff(b []byte) Format {
	has := func(off int, s string) bool { return len(b) >= off+len(s) && string(b[off:off+len(s)]) == s }
	switch {
	// Checked first: a tar header starts with a free-form file name that could mimic other signatures.
	case has(257, "ustar"):
		return TAR
	case has(4, "ftyp"):
		if len(b) >= 12 && slices.Contains(stillBrands, string(b[8:12])) {
			return unsupported
		}
		return MP4
	case has(0, "\x89PNG\r\n\x1a\n"):
		return PNG
	case has(0, "\xff\xd8\xff"):
		return JPEG
	case has(0, "GIF8"):
		return GIF
	case has(0, "RIFF") && has(8, "WEBP"):
		return WebP
	case has(0, "RIFF") && has(8, "WAVE"):
		return WAV
	case has(0, "II*\x00"), has(0, "MM\x00*"):
		return TIFF
	case has(0, "%PDF-"):
		return PDF
	case has(0, "PK\x03\x04"):
		return ZIP
	case has(0, "\x1f\x8b\x08"):
		return GZ
	case has(0, "7z\xbc\xaf\x27\x1c"):
		return SevenZ
	case has(0, "SQLite format 3\x00"):
		return SQLite
	case has(0, "fLaC"):
		return FLAC
	case has(0, "ID3"):
		return MP3
	case has(0, "OggS"):
		return OGG
	case has(0, "\x1a\x45\xdf\xa3"):
		return MKV
	// "BM", ICO and "MZ" are short; require a plausible header so text or random data does not match.
	case has(0, "BM") && len(b) >= 18 && slices.Contains([]uint32{12, 40, 52, 56, 64, 108, 124}, binary.LittleEndian.Uint32(b[14:])):
		return BMP
	case (has(0, "\x00\x00\x01\x00") || has(0, "\x00\x00\x02\x00")) && len(b) >= 6 && binary.LittleEndian.Uint16(b[4:]) > 0:
		return ICO
	case has(0, "MZ") && len(b) >= 64 && has(int(min(binary.LittleEndian.Uint32(b[60:]), 1<<20)), "PE\x00\x00"):
		return EXE
	}
	return ""
}

// readAt reads up to n bytes at off; a short file is not an error.
func readAt(path string, off int64, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readFile(f, off, n)
}

func readFile(f io.ReaderAt, off int64, n int) ([]byte, error) {
	b := make([]byte, n)
	m, err := f.ReadAt(b, off)
	if err == io.EOF {
		err = nil
	}
	return b[:m], err
}

// IsAPNG reports whether a PNG contains animation chunks (acTL, fcTL, fdAT).
func IsAPNG(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var h [8]byte
	if _, err := f.ReadAt(h[:], 0); err != nil || string(h[:]) != "\x89PNG\r\n\x1a\n" {
		return false
	}
	// Seek chunk to chunk instead of reading: image data can be large.
	for off := int64(8); ; off += 12 + int64(binary.BigEndian.Uint32(h[:4])) {
		if _, err := f.ReadAt(h[:], off); err != nil {
			return false
		}
		switch string(h[4:]) {
		case "acTL", "fcTL", "fdAT":
			return true
		case "IDAT", "IEND":
			return false
		}
	}
}

// IsNinePatch reports whether the file is an Android 9-patch image (*.9.png).
func IsNinePatch(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".9.png")
}

// IsJPEGCMYK reports whether a JPEG uses 4 color components.
func IsJPEGCMYK(path string) (cmyk bool) {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	jpegWalk(f, func(m byte, off int64, _ int) bool {
		if m < 0xc0 || m > 0xcf || m == 0xc4 || m == 0xc8 || m == 0xcc {
			return false
		}
		var b [10]byte
		_, err := f.ReadAt(b[:], off)
		cmyk = err == nil && b[9] == 4
		return true
	})
	return cmyk
}

// imageChunks calls fn with the type, data offset and size of each JPEG marker segment before the scan,
// PNG chunk before IEND or WebP chunk, until fn returns true. A JPEG type is its marker ("\xff\xe1").
// Chunks are seeked over, not read: image data can be large.
func imageChunks(f io.ReaderAt, fn func(typ string, off, n int64) bool) {
	h, _ := readFile(f, 0, 12)
	var c [8]byte
	switch {
	case bytes.HasPrefix(h, []byte("\xff\xd8")):
		jpegWalk(f, func(m byte, off int64, n int) bool { return fn(string([]byte{0xff, m}), off+4, int64(max(n-2, 0))) })
	case bytes.HasPrefix(h, []byte("\x89PNG\r\n\x1a\n")):
		for off := int64(8); ; off += 12 + int64(binary.BigEndian.Uint32(c[:])) {
			if _, err := f.ReadAt(c[:], off); err != nil || string(c[4:]) == "IEND" || fn(string(c[4:]), off+8, int64(binary.BigEndian.Uint32(c[:]))) {
				return
			}
		}
	case len(h) == 12 && string(h[:4]) == "RIFF" && string(h[8:]) == "WEBP":
		for off := int64(12); ; off += 8 + (int64(binary.LittleEndian.Uint32(c[4:]))+1)&^1 {
			if _, err := f.ReadAt(c[:], off); err != nil || fn(string(c[:4]), off+8, int64(binary.LittleEndian.Uint32(c[4:]))) {
				return
			}
		}
	}
}

// Orientation returns the EXIF Orientation tag of a JPEG, PNG (eXIf chunk) or WebP (EXIF chunk), or 0 if there is none.
// eXIf belongs before IDAT, but some writers put it after.
func Orientation(path string) (o int) {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	imageChunks(f, func(typ string, off, n int64) bool {
		if typ != "\xff\xe1" && typ != "eXIf" && typ != "EXIF" {
			return false
		}
		b, err := readFile(f, off, int(min(n, 1<<20)))
		if err != nil || typ == "\xff\xe1" && !bytes.HasPrefix(b, exifHeader) {
			return false
		}
		o = exifOrientation(b)
		return true
	})
	return o
}

// ColorManaged reports whether an image carries color data that makes a color-managed viewer display it other
// than as sRGB, which it assumes for untagged images: an ICC profile that isn't sRGB (JPEG APP2, PNG iCCP,
// WebP ICCP, TIFF, BMP, GIF), a PNG cICP chunk, a PNG gAMA chunk or non-sRGB cHRM chunk without an sRGB chunk,
// which takes precedence over them, or a calibrated or linked BMP color space.
// Stripping such data changes the displayed colors. For an ICO, it reports whether any embedded PNG does.
func ColorManaged(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h, _ := readFile(f, 0, 6)
	if len(h) < 6 || string(h[:4]) != "\x00\x00\x01\x00" && string(h[:4]) != "\x00\x00\x02\x00" {
		return colorManaged(f)
	}
	dir, _ := readFile(f, 6, 16*int(binary.LittleEndian.Uint16(h[4:])))
	for ; len(dir) >= 16; dir = dir[16:] {
		if colorManaged(io.NewSectionReader(f, int64(binary.LittleEndian.Uint32(dir[12:])), int64(binary.LittleEndian.Uint32(dir[8:])))) {
			return true
		}
	}
	return false
}

func colorManaged(f io.ReaderAt) bool {
	h, _ := readFile(f, 0, 138)
	switch {
	case bytes.HasPrefix(h, []byte("II*\x00")) || bytes.HasPrefix(h, []byte("MM\x00*")):
		bo, entries := tiffIFD0(f)
		for _, e := range entries {
			if bo.Uint16(e) == 34675 { // InterColorProfile
				p, _ := readFile(f, int64(bo.Uint32(e[8:])), int(min(bo.Uint32(e[4:]), 4<<20)))
				return !iccSRGB(p)
			}
		}
		return false
	// BITMAPV4HEADER and later have a color space type at offset 70 and, from V5 on, a profile at 126.
	case bytes.HasPrefix(h, []byte("BM")) && len(h) >= 122 && binary.LittleEndian.Uint32(h[14:]) >= 108:
		switch string(h[70:74]) {
		case "DEBM": // PROFILE_EMBEDDED
			if len(h) < 134 {
				return true
			}
			p, _ := readFile(f, 14+int64(binary.LittleEndian.Uint32(h[126:])), int(min(binary.LittleEndian.Uint32(h[130:]), 4<<20)))
			return !iccSRGB(p)
		case "KNIL": // PROFILE_LINKED
			return true
		case "\x00\x00\x00\x00": // LCS_CALIBRATED_RGB: endpoints and gamma
			return len(bytes.Trim(h[74:122], "\x00")) > 0
		}
		return false
	case bytes.HasPrefix(h, []byte("GIF8")):
		// The profile is an application extension, split into sub-blocks, before the image data.
		b, _ := readFile(f, 0, 1<<20)
		_, b, ok := bytes.Cut(b, []byte("\x21\xff\x0bICCRGBG1012"))
		if !ok {
			return false
		}
		var p []byte
		for len(b) > 0 && b[0] != 0 {
			n := int(b[0])
			if n >= len(b) {
				return true
			}
			p, b = append(p, b[1:1+n]...), b[1+n:]
		}
		return !iccSRGB(p)
	}
	var icc []byte
	var hasICC, srgb, tagged, cicp bool
	imageChunks(f, func(typ string, off, n int64) bool {
		switch typ {
		case "\xff\xe2", "iCCP", "ICCP":
			b, _ := readFile(f, off, int(min(n, 4<<20)))
			switch typ {
			case "\xff\xe2":
				// Large profiles span several segments, each with a sequence number and count.
				if p, ok := bytes.CutPrefix(b, []byte("ICC_PROFILE\x00")); ok && len(icc) < 4<<20 {
					hasICC, icc = true, append(icc, p[min(2, len(p)):]...)
				}
			case "ICCP":
				hasICC, icc = true, b
			case "iCCP":
				// Profile name, NUL, compression method, zlib data. A broken profile counts as not sRGB.
				hasICC = true
				if _, z, ok := bytes.Cut(b, []byte{0}); ok && len(z) > 1 {
					if r, err := zlib.NewReader(bytes.NewReader(z[1:])); err == nil {
						icc, _ = io.ReadAll(io.LimitReader(r, 4<<20))
					}
				}
			}
		case "sRGB":
			srgb = true
		case "gAMA":
			tagged = true
		case "cHRM":
			// ImageMagick writes sRGB primaries into every PNG. Without gAMA, viewers apply the sRGB curve.
			b, _ := readFile(f, off, int(min(n, 32)))
			tagged = tagged || !srgbChromaticities(b)
		case "cICP":
			cicp = true
		}
		// Color chunks precede the image data.
		return typ == "IDAT" || typ == "VP8 " || typ == "VP8L" || typ == "ANMF"
	})
	switch {
	case cicp:
		return true
	case hasICC:
		return !iccSRGB(icc)
	}
	return tagged && !srgb
}

// srgbChromaticities reports whether PNG cHRM data holds the sRGB white point and primaries, to within 0.001.
func srgbChromaticities(b []byte) bool {
	if len(b) != 32 {
		return false
	}
	for i, v := range []int64{31270, 32900, 64000, 33000, 30000, 60000, 15000, 6000} {
		if d := int64(binary.BigEndian.Uint32(b[4*i:])) - v; d < -100 || d > 100 {
			return false
		}
	}
	return true
}

// srgbD50 holds the sRGB colorants adapted to the D50 profile connection space, as in ICC matrix profiles.
var srgbD50 = [3][3]float64{{0.4361, 0.2225, 0.0139}, {0.3851, 0.7169, 0.0971}, {0.1431, 0.0606, 0.7141}}

// iccSRGB reports whether an ICC profile is an RGB matrix/TRC profile with the sRGB colorants and tone curve,
// so it displays like an untagged image. The description can't be trusted: "Linear sRGB" and gamma 1.8
// "sRGB" profiles exist, and some sRGB profiles have a meaningless name.
func iccSRGB(p []byte) bool {
	if len(p) < 132 || string(p[16:20]) != "RGB " {
		return false
	}
	tags := map[string][]byte{}
	for n, t := binary.BigEndian.Uint32(p[128:]), p[132:]; n > 0 && len(t) >= 12; n, t = n-1, t[12:] {
		off, size := uint64(binary.BigEndian.Uint32(t[4:])), uint64(binary.BigEndian.Uint32(t[8:]))
		if off+size > uint64(len(p)) {
			return false
		}
		tags[string(t[:4])] = p[off : off+size]
	}
	// Color-managed viewers use a lookup table in preference to the matrix.
	for _, a2b := range []string{"A2B0", "A2B1", "A2B2"} {
		if _, ok := tags[a2b]; ok {
			return false
		}
	}
	for i, c := range []string{"rXYZ", "gXYZ", "bXYZ"} {
		b := tags[c]
		if len(b) < 20 || string(b[:4]) != "XYZ " {
			return false
		}
		for j, want := range srgbD50[i] {
			if math.Abs(s15Fixed16(b[8+4*j:])-want) > 0.005 {
				return false
			}
		}
	}
	for _, c := range []string{"rTRC", "gTRC", "bTRC"} {
		// Compared in encoded values, where a difference of 0.004 is one 8-bit step.
		for i := 1; i <= 20; i++ {
			x := float64(i) / 20
			y, ok := toneCurve(tags[c], x)
			if d := math.Abs(srgbEncode(y) - x); !ok || !(d <= 0.004) {
				return false
			}
		}
	}
	return true
}

func s15Fixed16(b []byte) float64 { return float64(int32(binary.BigEndian.Uint32(b))) / 65536 }

func srgbEncode(y float64) float64 {
	if y <= 0.0031308 {
		return 12.92 * y
	}
	return 1.055*math.Pow(y, 1/2.4) - 0.055
}

// toneCurve evaluates an ICC 'curv' or 'para' tone curve at x in [0,1].
func toneCurve(b []byte, x float64) (float64, bool) {
	if len(b) < 12 {
		return 0, false
	}
	n := int(binary.BigEndian.Uint32(b[8:]))
	switch string(b[:4]) {
	case "curv":
		switch {
		case n == 0:
			return x, true
		case n == 1 && len(b) >= 14:
			return math.Pow(x, float64(binary.BigEndian.Uint16(b[12:]))/256), true
		case n < 2 || n > (len(b)-12)/2:
			return 0, false
		}
		t := func(i int) float64 { return float64(binary.BigEndian.Uint16(b[12+2*i:])) / 65535 }
		pos := x * float64(n-1)
		i := min(int(pos), n-2)
		return t(i) + (t(i+1)-t(i))*(pos-float64(i)), true
	case "para":
		typ := int(binary.BigEndian.Uint16(b[8:]))
		if typ > 4 || len(b) < 12+4*[]int{1, 3, 4, 5, 7}[typ] {
			return 0, false
		}
		// Y = (aX+b)^g + e for X >= d, else cX + f. Type 0 has only g; types 1 and 2 switch where aX+b = 0,
		// type 2 adds c to both branches.
		var v [7]float64
		for i := range []int{1, 3, 4, 5, 7}[typ] {
			v[i] = s15Fixed16(b[12+4*i:])
		}
		g, a, bb, c, d, e, f := v[0], v[1], v[2], v[3], v[4], v[5], v[6]
		switch typ {
		case 0:
			return math.Pow(x, g), true
		case 1, 2:
			if a*x+bb < 0 {
				return c, true
			}
			return math.Pow(a*x+bb, g) + c, true
		}
		if x < d {
			return c*x + f, true
		}
		return math.Pow(a*x+bb, g) + e, true
	}
	return 0, false
}

// IsAnimatedWebP reports whether a WebP has the animation flag set or holds animation chunks.
func IsAnimatedWebP(path string) (anim bool) {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	imageChunks(f, func(typ string, off, n int64) bool {
		if typ == "VP8X" {
			b, _ := readFile(f, off, 1)
			anim = len(b) == 1 && b[0]&2 != 0
		}
		anim = anim || typ == "ANIM" || typ == "ANMF"
		return anim || typ == "VP8 " || typ == "VP8L"
	})
	return anim
}

// HasGainMap reports whether a JPEG carries an HDR gain map: Ultra HDR / Adobe (hdrgm XMP), ISO 21496-1 or
// Apple (HDRGainMap XMP in the secondary image). The gain map is a second JPEG after the end of the primary one.
func HasGainMap(path string) bool {
	if h, _ := readAt(path, 0, 2); string(h) != "\xff\xd8" {
		return false
	}
	return slices.Contains(contains(path, "hdrgm", "urn:iso:std:iso:ts:21496:-1", "HDRGainMap"), true)
}

var exifHeader = []byte("Exif\x00\x00")

// exifOrientation reads the Orientation tag from EXIF data: a TIFF structure, in JPEG preceded by "Exif\0\0".
// Some PNG and WebP writers add that header too.
func exifOrientation(b []byte) (o int) {
	bo, entries := tiffIFD0(bytes.NewReader(bytes.TrimPrefix(b, exifHeader)))
	for _, e := range entries {
		if bo.Uint16(e) == 0x0112 && bo.Uint16(e[2:]) == 3 {
			o = int(bo.Uint16(e[8:]))
		}
	}
	return o
}

// jpegWalk calls fn for each marker segment before the scan data until fn returns true.
// It walks the segments rather than searching for marker bytes, which also occur inside EXIF thumbnails.
func jpegWalk(f io.ReaderAt, fn func(marker byte, off int64, n int) bool) {
	var b [4]byte
	if _, err := f.ReadAt(b[:2], 0); err != nil || string(b[:2]) != "\xff\xd8" {
		return
	}
	for off := int64(2); ; {
		if _, err := f.ReadAt(b[:], off); err != nil || b[0] != 0xff {
			return
		}
		switch m := b[1]; {
		case m == 0xff:
			off++
			continue
		case m == 0x01 || m >= 0xd0 && m <= 0xd7:
			off += 2
			continue
		case m == 0xda || m == 0xd9:
			return
		}
		n := int(binary.BigEndian.Uint16(b[2:]))
		if fn(b[1], off, n) {
			return
		}
		off += 2 + int64(n)
	}
}

// tiffIFD0 returns the byte order and the 12 byte entries of the first IFD of a classic TIFF.
func tiffIFD0(r io.ReaderAt) (binary.ByteOrder, [][]byte) {
	var h [8]byte
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return nil, nil
	}
	var bo binary.ByteOrder
	switch string(h[:4]) {
	case "II*\x00":
		bo = binary.LittleEndian
	case "MM\x00*":
		bo = binary.BigEndian
	default:
		return nil, nil
	}
	ifd := int64(bo.Uint32(h[4:]))
	var n [2]byte
	if _, err := r.ReadAt(n[:], ifd); err != nil {
		return nil, nil
	}
	b, err := readFile(r, ifd+2, 12*int(bo.Uint16(n[:])))
	if err != nil {
		return nil, nil
	}
	var entries [][]byte
	for ; len(b) >= 12; b = b[12:] {
		entries = append(entries, b[:12])
	}
	return bo, entries
}

// TIFFTags lists the tags of the first IFD of a TIFF; nil if it can't be read (including BigTIFF).
func TIFFTags(path string) []uint16 {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	bo, entries := tiffIFD0(f)
	if bo == nil {
		return nil
	}
	tags := make([]uint16, 0, len(entries))
	for _, e := range entries {
		tags = append(tags, bo.Uint16(e))
	}
	return tags
}

// IsPDFLayered reports whether a PDF has optional content groups (layers).
func IsPDFLayered(path string) bool {
	b, _ := readAt(path, 0, 512<<10)
	return bytes.Contains(b, []byte("/OCG"))
}

// IsPDFSigned reports whether a PDF carries a digital signature, document timestamp or certification (DocMDP)
// permissions. A signature covers the exact file bytes, so any rewrite invalidates it. Signature dictionaries
// can't be compressed, because the signer patches /ByteRange and /Contents in place, but the /Sig field
// referring to one can be, so /ByteRange alone counts.
func IsPDFSigned(path string) bool {
	return slices.Contains(contains(path, "/ByteRange", "/DocMDP"), true)
}

// IsPDFA reports whether a PDF declares PDF/A conformance in its XMP metadata.
func IsPDFA(path string) bool {
	return contains(path, "pdfaid:part")[0]
}

// contains reports for each pattern whether it occurs anywhere in the file.
func contains(path string, pats ...string) []bool {
	found := make([]bool, len(pats))
	f, err := os.Open(path)
	if err != nil {
		return found
	}
	defer f.Close()
	keep := 0
	for _, p := range pats {
		keep = max(keep, len(p)-1)
	}
	buf := make([]byte, 0, 1<<20)
	for {
		n, err := io.ReadFull(f, buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		for i, p := range pats {
			found[i] = found[i] || bytes.Contains(buf, []byte(p))
		}
		if err != nil || !slices.Contains(found, false) {
			return found
		}
		// Keep the tail so a pattern spanning two reads is still found.
		buf = buf[:copy(buf, buf[max(len(buf)-keep, 0):])]
	}
}

var (
	installerMarks = []string{"Inno Setup", "InstallShield", "Nullsoft Install System", "RTPatch", "MSCF"}
	archiveMarks   = []string{"Rar!\x1a\x07", "PK\x03\x04", "7z\xbc\xaf\x27\x1c"}
)

// IsSFX reports whether a PE file is a self-extracting archive or installer.
func IsSFX(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head, _ := readFile(f, 0, 1<<20)
	if !bytes.HasPrefix(head, []byte("MZ")) {
		return false
	}
	if slices.ContainsFunc(installerMarks, func(m string) bool { return bytes.Contains(head, []byte(m)) }) {
		return true
	}
	// SFX archives are appended after the PE image. Searching only that overlay avoids
	// flagging tools that merely embed archive signatures as constants (zip/7z handlers).
	tail := head
	if p, err := pe.NewFile(f); err == nil {
		var end int64
		for _, s := range p.Sections {
			end = max(end, int64(s.Offset)+int64(s.Size))
		}
		tail, _ = readFile(f, end, 1<<20)
	}
	return slices.ContainsFunc(archiveMarks, func(m string) bool { return bytes.Contains(tail, []byte(m)) })
}

// IsSigned reports whether a PE file carries an Authenticode signature.
func IsSigned(path string) bool {
	p, err := pe.Open(path)
	if err != nil {
		return false
	}
	defer p.Close()
	return certificate(p).Size > 0
}

// certificate returns the security data directory; its address is a file offset, not an RVA.
func certificate(p *pe.File) pe.DataDirectory {
	const security = pe.IMAGE_DIRECTORY_ENTRY_SECURITY
	switch h := p.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		if h.NumberOfRvaAndSizes > security {
			return h.DataDirectory[security]
		}
	case *pe.OptionalHeader64:
		if h.NumberOfRvaAndSizes > security {
			return h.DataDirectory[security]
		}
	}
	return pe.DataDirectory{}
}

// HasPEOverlay reports whether a PE file has non-zero data after its sections, COFF symbol table and
// certificate. PyInstaller, .NET single-file apps and installers keep their payload there.
func HasPEOverlay(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	p, err := pe.NewFile(f)
	if err != nil || len(p.Sections) == 0 {
		return false
	}
	var end int64
	for _, s := range p.Sections {
		end = max(end, int64(s.Offset)+int64(s.Size))
	}
	if h := p.FileHeader; h.PointerToSymbolTable != 0 {
		// The string table follows the symbols and starts with its own size.
		st := int64(h.PointerToSymbolTable) + 18*int64(h.NumberOfSymbols)
		if n, err := readFile(f, st, 4); err == nil && len(n) == 4 {
			end = max(end, st+int64(binary.LittleEndian.Uint32(n)))
		}
	}
	if c := certificate(p); c.Size > 0 {
		end = max(end, int64(c.VirtualAddress)+int64(c.Size))
	}
	buf := make([]byte, 64<<10)
	for off := end; ; off += int64(len(buf)) {
		n, err := f.ReadAt(buf, off)
		if len(bytes.TrimLeft(buf[:n], "\x00")) > 0 {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// IsFragmentedMP4 reports whether an ISO-BMFF file has movie fragments (moof boxes, or mvex in moov).
func IsFragmentedMP4(path string) (frag bool) {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	mp4Boxes(f, 0, fi.Size(), func(typ string, start, end int64) bool {
		switch typ {
		case "moof":
			frag = true
		case "moov":
			mp4Boxes(f, start, end, func(typ string, _, _ int64) bool {
				frag = typ == "mvex"
				return frag
			})
		}
		return frag
	})
	return frag
}

// mp4Boxes calls fn with the type and payload range of each box in [off, end) until fn returns true.
func mp4Boxes(f io.ReaderAt, off, end int64, fn func(typ string, start, end int64) bool) {
	var h [16]byte
	for off >= 0 && off+8 <= end {
		if _, err := f.ReadAt(h[:8], off); err != nil {
			return
		}
		n, hdr := int64(binary.BigEndian.Uint32(h[:])), int64(8)
		switch n {
		case 0:
			n = end - off
		case 1:
			if _, err := f.ReadAt(h[8:], off+8); err != nil {
				return
			}
			n, hdr = int64(binary.BigEndian.Uint64(h[8:])), 16
		}
		if n < hdr || fn(string(h[4:8]), off+hdr, min(off+n, end)) {
			return
		}
		off += n
	}
}

var oggCodecs = map[string]string{
	"\x01vorbis": "vorbis", "\x80theora": "theora", "OpusHead": "opus",
	"\x7fFLAC": "flac", "Speex   ": "speex", "fishead\x00": "skeleton",
}

// OggCodecs lists the codec of each logical stream of an Ogg file, identified by the first packet of
// its beginning-of-stream page: "vorbis", "theora", "opus", "flac", "speex", "skeleton", or "" if unknown.
// A broken page structure also yields "", since it could hide further streams.
func OggCodecs(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var codecs []string
	b := make([]byte, 27+255+8)
	for off := int64(0); ; {
		n, _ := f.ReadAt(b, off)
		if n == 0 && off > 0 {
			return codecs
		}
		if n < 27 || string(b[:4]) != "OggS" || n < 27+int(b[26]) {
			return append(codecs, "")
		}
		segs, size := int(b[26]), 0
		for _, s := range b[27 : 27+segs] {
			size += int(s)
		}
		if b[5]&2 != 0 {
			c := ""
			for id, name := range oggCodecs {
				if bytes.HasPrefix(b[27+segs:n], []byte(id)) {
					c = name
				}
			}
			codecs = append(codecs, c)
		}
		off += int64(27 + segs + size)
	}
}

// Matroska elements that mkclean drops unless it writes DocType version 4, which ffmpeg can't read.
var matroskaV4IDs = []uint64{
	0x55b0, 0x7670, 0x9d, // Video: Colour, Projection, FieldOrder
	0x56aa, 0x56bb, 0x41e4, // TrackEntry: CodecDelay, SeekPreRoll, BlockAdditionMapping
}

// MatroskaV4 reports whether the tracks of a Matroska file use elements introduced by Matroska v4.
// It also reports true if the tracks can't be found before the first cluster.
func MatroskaV4(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	elem := func(off int64) (id uint64, size int64, hdr int) {
		b, _ := readFile(f, off, 12)
		return ebmlElem(b)
	}
	id, size, hdr := elem(0)
	if id != 0x1a45dfa3 || size < 0 {
		return true
	}
	off := int64(hdr) + size
	if id, _, hdr = elem(off); id != 0x18538067 {
		return true
	}
	for off += int64(hdr); ; {
		id, size, hdr = elem(off)
		switch {
		case hdr == 0 || size < 0 || id == 0x1f43b675: // Cluster
			return true
		case id == 0x1654ae6b: // Tracks
			b, err := readFile(f, off+int64(hdr), int(min(size, 16<<20)))
			return err != nil || int64(len(b)) != size || matroskaV4(b)
		}
		off += int64(hdr) + size
	}
}

func matroskaV4(b []byte) bool {
	for len(b) > 0 {
		id, size, hdr := ebmlElem(b)
		if hdr == 0 || size < 0 || size > int64(len(b)-hdr) {
			return true
		}
		e := b[hdr : hdr+int(size)]
		// TrackEntry and Video hold the v4 elements.
		if slices.Contains(matroskaV4IDs, id) || (id == 0xae || id == 0xe0) && matroskaV4(e) {
			return true
		}
		b = b[hdr+int(size):]
	}
	return false
}

// ebmlElem decodes an element header; size is -1 for unknown, hdr is 0 if b holds no valid header.
func ebmlElem(b []byte) (id uint64, size int64, hdr int) {
	id, n := vint(b)
	if n == 0 || n > 4 {
		return 0, 0, 0
	}
	s, m := vint(b[n:])
	if m == 0 {
		return 0, 0, 0
	}
	if s &^= 1 << (7 * m); s == 1<<(7*m)-1 {
		return id, -1, n + m
	}
	return id, int64(s), n + m
}

// vint returns a variable length EBML integer including its length marker bit.
func vint(b []byte) (v uint64, n int) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0
	}
	n = bits.LeadingZeros8(b[0]) + 1
	if len(b) < n {
		return 0, 0
	}
	for _, c := range b[:n] {
		v = v<<8 | uint64(c)
	}
	return v, n
}

// HasZIPUnicodeExtra reports whether a ZIP keeps file names or comments in Info-ZIP Unicode extra fields.
// The optimizers drop extra fields, which would revert those names to their legacy encoding.
func HasZIPUnicodeExtra(path string) bool {
	r, err := zip.OpenReader(path)
	if err != nil {
		return false
	}
	defer r.Close()
	for _, f := range r.File {
		for e := f.Extra; len(e) >= 4; e = e[min(4+int(binary.LittleEndian.Uint16(e[2:])), len(e)):] {
			if id := binary.LittleEndian.Uint16(e); id == 0x7075 || id == 0x6375 {
				return true
			}
		}
	}
	return false
}

// HasZIPDirs reports whether a ZIP has directory entries. Unreadable files count as having them.
func HasZIPDirs(path string) bool {
	r, err := zip.OpenReader(path)
	if err != nil {
		return true
	}
	defer r.Close()
	return slices.ContainsFunc(r.File, func(f *zip.File) bool { return strings.HasSuffix(f.Name, "/") })
}
