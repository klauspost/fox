package pipeline

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"debug/pe"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/klauspost/fox/internal/config"
	"github.com/klauspost/fox/internal/detect"
)

func TestLevels(t *testing.T) {
	want := map[string][9]int{
		"iter":    {1, 1, 2, 3, 6, 9, 14, 21, 30},
		"ect":     {1, 2, 3, 4, 5, 6, 7, 8, 90032},
		"pingo":   {0, 0, 1, 1, 2, 2, 3, 3, 4},
		"oxipng":  {0, 1, 2, 2, 3, 4, 4, 5, 6},
		"truepng": {1, 1, 2, 2, 2, 3, 3, 3, 4},
		"pngout":  {3, 3, 2, 2, 2, 1, 1, 1, 0},
	}
	fns := map[string]func(int) int{"iter": iter, "ect": ectLevel, "pingo": pingoLevel,
		"oxipng": oxipngLevel, "truepng": truepngLevel, "pngout": pngoutLevel}
	for name, w := range want {
		for l := 1; l <= 9; l++ {
			if got := fns[name](l); got != w[l-1] {
				t.Errorf("%s(%d) = %d, want %d", name, l, got, w[l-1])
			}
		}
	}
	for l, w := range map[int]string{1: "-1", 3: "-9", 5: "-9 --best", 7: "-9 --best --lzma", 9: "-9 --best --lzma --ultra-brute"} {
		if got := upxFlags(l); got != w {
			t.Errorf("upxFlags(%d) = %q, want %q", l, got, w)
		}
	}
	for l, w := range map[int]string{2: "-1", 4: "-8 --best", 6: "-8 --best -e", 9: "-8 --best -ep"} {
		if got := flacFlags(l); got != w {
			t.Errorf("flacFlags(%d) = %q, want %q", l, got, w)
		}
	}
}

// Fixtures

func chunk(typ string, data []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	return append(append(append(b, typ...), data...), 0, 0, 0, 0)
}

func pngFile(animated bool) []byte {
	b := append([]byte("\x89PNG\r\n\x1a\n"), chunk("IHDR", make([]byte, 13))...)
	if animated {
		b = append(b, chunk("acTL", make([]byte, 8))...)
	}
	return append(append(b, chunk("IDAT", nil)...), chunk("IEND", nil)...)
}

func jpegFile(components byte) []byte {
	return []byte{0xff, 0xd8, 0xff, 0xc0, 0, 17, 8, 0, 1, 0, 1, components, 1, 0x11, 0, 2, 0x11, 0, 3, 0x11, 0, 4, 0x11, 0, 0xff, 0xd9}
}

// rotatedJPEG has an EXIF Orientation of 6 (rotate 90 degrees clockwise).
func rotatedJPEG() []byte {
	exif := "Exif\x00\x00" + orientation6
	app1 := append([]byte{0xff, 0xe1, 0, byte(2 + len(exif))}, exif...)
	j := jpegFile(3)
	return slices.Concat(j[:2], app1, j[2:])
}

// orientation6 is little endian EXIF data with an Orientation of 6.
const orientation6 = "II*\x00\x08\x00\x00\x00\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00\x06\x00\x00\x00\x00\x00\x00\x00"

func rotatedPNG() []byte {
	p := pngFile(false)
	return slices.Concat(p[:33], chunk("eXIf", []byte(orientation6)), p[33:])
}

func rotatedWebP() []byte {
	body := "WEBPVP8X\x0a\x00\x00\x00\x08\x00\x00\x00\x00\x00\x00\x00\x00\x00EXIF\x1a\x00\x00\x00" + orientation6
	return []byte("RIFF" + string(binary.LittleEndian.AppendUint32(nil, uint32(len(body)))) + body)
}

// iccProfile is an RGB matrix/TRC profile with the sRGB colorants and either the sRGB tone curve or gamma 1.8.
func iccProfile(srgb bool) string {
	u32 := func(v int) string { return string(binary.BigEndian.AppendUint32(nil, uint32(v))) }
	s15 := func(v float64) string { return u32(int(math.Round(v * 65536))) }
	trc := "curv" + u32(0) + u32(1) + "\x01\xcd\x00\x00"
	if srgb {
		trc = "para" + u32(0) + "\x00\x03\x00\x00" + s15(2.4) + s15(1/1.055) + s15(0.055/1.055) + s15(1/12.92) + s15(0.04045)
	}
	tags := []string{"rXYZ", "gXYZ", "bXYZ", "rTRC", "gTRC", "bTRC"}
	data := []string{
		"XYZ " + u32(0) + s15(0.4361) + s15(0.2225) + s15(0.0139),
		"XYZ " + u32(0) + s15(0.3851) + s15(0.7169) + s15(0.0971),
		"XYZ " + u32(0) + s15(0.1431) + s15(0.0606) + s15(0.7141),
		trc, trc, trc,
	}
	table, body := u32(len(tags)), ""
	for i, t := range tags {
		table += t + u32(128+4+12*len(tags)+len(body)) + u32(len(data[i]))
		body += data[i]
	}
	return strings.Repeat("\x00", 16) + "RGB " + strings.Repeat("\x00", 108) + table + body
}

func iccJPEG(srgb bool) []byte {
	seg := "ICC_PROFILE\x00\x01\x01" + iccProfile(srgb)
	j := jpegFile(3)
	return slices.Concat(j[:2], []byte{0xff, 0xe2}, binary.BigEndian.AppendUint16(nil, uint16(2+len(seg))), []byte(seg), j[2:])
}

// colorPNG is a PNG with an extra chunk before the image data.
func colorPNG(typ, data string) []byte {
	p := pngFile(false)
	return slices.Concat(p[:33], chunk(typ, []byte(data)), p[33:])
}

func riffChunk(typ, data string) string {
	return typ + string(binary.LittleEndian.AppendUint32(nil, uint32(len(data)))) + data + strings.Repeat("\x00", len(data)%2)
}

func webpFile(chunks ...string) []byte {
	body := "WEBP" + strings.Join(chunks, "")
	return []byte("RIFF" + string(binary.LittleEndian.AppendUint32(nil, uint32(len(body)))) + body)
}

// tiffFile is a little endian TIFF whose first IFD holds the given tags.
func tiffFile(tags ...uint16) []byte {
	b := binary.LittleEndian.AppendUint16([]byte("II*\x00\x08\x00\x00\x00"), uint16(len(tags)))
	for _, t := range tags {
		b = append(binary.LittleEndian.AppendUint16(b, t), make([]byte, 10)...)
	}
	return b
}

func oggPage(bos bool, data string) string {
	h := []byte("OggS\x00\x00")
	if bos {
		h[5] = 2
	}
	h = append(h, make([]byte, 20)...)
	return string(append(h, 1, byte(len(data)))) + data
}

func ogg(firstPackets ...string) []byte {
	var s string
	for _, p := range firstPackets {
		s += oggPage(true, p)
	}
	return []byte(s + oggPage(false, "data"))
}

func box(typ string, payload ...string) string {
	p := strings.Join(payload, "")
	return string(binary.BigEndian.AppendUint32(nil, uint32(8+len(p)))) + typ + p
}

// ebml encodes an element with a 1 byte size; payloads must be shorter than 127 bytes.
func ebml(id uint32, payload ...string) string {
	p := strings.Join(payload, "")
	idb := binary.BigEndian.AppendUint32(nil, id)
	for idb[0] == 0 {
		idb = idb[1:]
	}
	return string(idb) + string([]byte{0x80 | byte(len(p))}) + p
}

// mkvFile is a Matroska file with one track entry holding the given elements.
func mkvFile(entry ...string) []byte {
	return []byte(ebml(0x1a45dfa3, ebml(0x4282, "matroska")) +
		ebml(0x18538067, ebml(0x1654ae6b, ebml(0xae, entry...)), ebml(0x1f43b675)))
}

func zipFile(extra []byte, dirs ...string) []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "caf?.txt", Extra: extra})
	w.Write([]byte("x"))
	for _, d := range dirs {
		zw.Create(d)
	}
	zw.Close()
	return b.Bytes()
}

// peFile builds a minimal PE32+ image; signed sets the security directory, extra is appended.
func peFile(signed bool, extra string, dllChars uint16) []byte {
	var b bytes.Buffer
	dos := make([]byte, 0x40)
	copy(dos, "MZ")
	binary.LittleEndian.PutUint32(dos[0x3c:], 0x40)
	b.Write(dos)
	b.WriteString("PE\x00\x00")
	oh := pe.OptionalHeader64{Magic: 0x20b, NumberOfRvaAndSizes: 16, DllCharacteristics: dllChars}
	if signed {
		oh.DataDirectory[pe.IMAGE_DIRECTORY_ENTRY_SECURITY] = pe.DataDirectory{VirtualAddress: 0x1000, Size: 8}
	}
	binary.Write(&b, binary.LittleEndian, pe.FileHeader{Machine: pe.IMAGE_FILE_MACHINE_AMD64, SizeOfOptionalHeader: uint16(binary.Size(oh))})
	binary.Write(&b, binary.LittleEndian, oh)
	b.WriteString(extra)
	return b.Bytes()
}

const nothing = "nothing to do with current settings"

func gz(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

// render formats steps as "tool args..." for comparison.
func render(steps []Step) []string {
	var r []string
	for _, s := range steps {
		switch {
		case s.Func != nil && len(s.Args) == 0:
			r = append(r, s.Tool+" <func>")
		case s.Stdio:
			r = append(r, s.Tool+" <stdio>")
		default:
			r = append(r, strings.TrimSpace(s.Tool+" "+strings.Join(s.Args, " ")))
		}
	}
	return r
}

// remuxTools must reject results below half the current size.
var remuxTools = []string{"ffmpeg", "mkclean", "mp4file", "rehuff_theora"}

func TestFor(t *testing.T) {
	plainPNG, apng := pngFile(false), pngFile(true)
	mkv := mkvFile(ebml(0x86, "V_MPEG4/ISO/AVC"), ebml(0xe0, ebml(0xb0, "\x01\x40")))
	tests := []struct {
		name     string
		f        detect.Format
		file     string
		data     []byte
		set      func(s *config.Settings)
		skip     string
		tools    []string // exact tool sequence, if set
		has      []string // rendered steps that must be present
		hasNot   []string // substrings no step may contain
		sidecars []string // suffixes of extra empty files next to the input
	}{
		{
			name: "jpeg level 5 lossless", f: detect.JPEG, file: "a.jpg", data: jpegFile(3),
			tools: []string{"pingo", "jhead", "leanify", "jpegoptim", "jpegtran", "mozjpegtran", "ect"},
			has: []string{
				"pingo -s2 -lossless {in}",
				"jhead -q -purejpg -di -dx -dt -zt {in}",
				"leanify -q -p -i 6 {in}",
				"jpegoptim -o -q --all-progressive --nofix --strip-all -w 1 {in}",
				"jpegtran -outfile {out} -progressive -optimize -copy none {in}",
				"mozjpegtran -outfile {out} -progressive -optimize -perfect -copy none {in}",
				"ect -quiet --mt-deflate --allfilters -progressive -strip --strict -5 {in}",
			},
			hasNot: []string{"autorot"},
		},
		{
			name: "jpeg level 9 lossy", f: detect.JPEG, file: "a.jpg", data: jpegFile(3),
			set:   func(s *config.Settings) { s.Level, s.JPEG.Lossy = 9, true },
			tools: []string{"pingo", "cjpegli", "guetzli", "jpeg-recompress", "jhead", "leanify", "magick", "jpegoptim", "jpegtran", "mozjpegtran", "ect"},
			has: []string{
				"pingo -s4 -quality=95 {in}",
				"cjpegli -q 70 {in} {out}",
				"guetzli --auto --quality 90 {in} {out}",
				"jpeg-recompress --method smallfry --quality high --min 60 --subsample disable --quiet --strip --accurate {in} {out}",
				"leanify -q -p -i 30 {in}",
				"magick -quiet {in} -interlace Plane -define jpeg:optimize-coding=true -strip {out}",
				"ect -quiet --mt-deflate --allfilters -progressive -strip -90032 {in}",
			},
			hasNot: []string{"--strict"},
		},
		{
			name: "jpeg level 1 lossy metadata arithmetic", f: detect.JPEG, file: "a.jpg", data: jpegFile(3),
			set: func(s *config.Settings) {
				s.Level, s.LeanifyIterations = 1, 4
				s.JPEG.Lossy, s.JPEG.KeepMetadata, s.JPEG.Arithmetic = true, true, true
			},
			tools: []string{"jpeg-recompress", "leanify", "magick", "jpegoptim", "jpegtran", "mozjpegtran", "ect"},
			has: []string{
				"jpeg-recompress --method smallfry --quality high --min 60 --subsample disable --quiet {in} {out}",
				"leanify -q -p --keep-exif --keep-icc --jpeg-keep-all --jpeg-arithmetic -i 4 {in}",
				"jpegoptim -o -q --all-progressive --nofix --all-arith -w 1 {in}",
				"jpegtran -outfile {out} -progressive -optimize -arithmetic -copy all {in}",
				"ect -quiet --mt-deflate --allfilters -progressive -1 {in}",
			},
			hasNot: []string{"-strip", "--strict"},
		},
		{
			name: "rotated jpeg keeps metadata", f: detect.JPEG, file: "a.jpg", data: rotatedJPEG(),
			set:   func(s *config.Settings) { s.JPEG.Lossy = true },
			tools: []string{"jpeg-recompress", "leanify", "magick", "jpegoptim", "jpegtran", "mozjpegtran", "ect"},
			has: []string{
				"leanify -q -p --keep-exif --keep-icc --jpeg-keep-all -i 6 {in}",
				"mozjpegtran -outfile {out} -progressive -optimize -perfect -copy all {in}",
			},
			hasNot: []string{"-strip", "-purejpg"},
		},
		{
			name: "color-managed jpeg keeps metadata", f: detect.JPEG, file: "a.jpg", data: iccJPEG(false),
			tools:  []string{"leanify", "jpegoptim", "jpegtran", "mozjpegtran", "ect"},
			has:    []string{"leanify -q -p --keep-exif --keep-icc --jpeg-keep-all -i 6 {in}"},
			hasNot: []string{"-strip", "-copy none"},
		},
		{
			name: "srgb jpeg strips", f: detect.JPEG, file: "a.jpg", data: iccJPEG(true),
			tools: []string{"pingo", "jhead", "leanify", "jpegoptim", "jpegtran", "mozjpegtran", "ect"},
			has:   []string{"jpegtran -outfile {out} -progressive -optimize -copy none {in}"},
		},
		{
			name: "ultra hdr jpeg", f: detect.JPEG, file: "a.jpg", skip: "HDR gain map JPEG",
			data: slices.Concat(jpegFile(3), []byte(`<x:xmpmeta hdrgm:Version="1.0"/>`), jpegFile(3)),
		},
		{
			name: "cmyk jpeg skips jhead", f: detect.JPEG, file: "a.jpg", data: jpegFile(4),
			tools: []string{"pingo", "leanify", "jpegoptim", "jpegtran", "mozjpegtran", "ect"},
		},
		{
			name: "png level 5", f: detect.PNG, file: "a.png", data: plainPNG,
			tools: []string{"pngoptimizer", "truepng", "pngout", "oxipng", "leanify", "pngwolf", "pngrewrite", "advpng", "ect", "pingo", "deflopt", "defluff", "deflopt"},
			has: []string{
				"pngoptimizer -file:{in}",
				"truepng -o2 -tz -md remove all -a1 -g0 /i0 /nc /tz /quiet /y /out {out} {in}",
				"pngout /q /y /r /d0 /mincodes0 /kacTL,fcTL,fdAT /s2 {in} {out}",
				"oxipng --zopfli --alpha --quiet -o3 --strip all {in}",
				"pngwolf --out-deflate=zopfli,iter=6 --in={in} --out={out}",
				"pngrewrite {in} {out}",
				"advpng -z -q -4 -i 6 {in}",
				"ect -quiet --mt-deflate --allfilters -strip -5 {in}",
				"pingo -s2 -lossless {in}",
				"deflopt /a /b /s {in}",
				"defluff <stdio>",
			},
		},
		{
			name: "png level 9 lossy metadata", f: detect.PNG, file: "a.png", data: plainPNG,
			set: func(s *config.Settings) {
				s.Level, s.PNGWolfIterations = 9, 7
				s.PNG.Lossy, s.PNG.KeepMetadata = true, true
			},
			tools: []string{"truepng", "pngout", "oxipng", "pngwolf", "ect", "pingo", "deflopt", "defluff", "deflopt"},
			has: []string{
				"truepng -o4 -md keep all -l /i0 /nc /tz /quiet /y /out {out} {in}",
				"pngout /q /y /r /d0 /mincodes0 /k1 /s0 {in} {out}",
				"oxipng --zopfli --alpha --quiet -o6 --scale16 {in}",
				"pngwolf --out-deflate=zopfli,iter=7 --in={in} --out={out}",
				"ect -quiet --mt-deflate --allfilters -90032 {in}",
				"pingo -s4 -nostrip -quality=95 {in}",
				"deflopt /a /b /s /k {in}",
			},
			hasNot: []string{"--strip"},
		},
		{
			name: "png lossy strips in pngquant", f: detect.PNG, file: "a.png", data: plainPNG,
			set: func(s *config.Settings) { s.PNG.Lossy = true },
			has: []string{"pngquant --strip --quality=85-95 --speed 1 --force --output {out} {in}"},
		},
		{
			name: "rotated png keeps metadata", f: detect.PNG, file: "a.png", data: rotatedPNG(),
			set:    func(s *config.Settings) { s.PNG.Lossy = true },
			tools:  []string{"truepng", "pngout", "oxipng", "pngwolf", "ect", "pingo", "deflopt", "defluff", "deflopt"},
			hasNot: []string{"--strip", "remove"},
		},
		{
			name: "png gamma keeps metadata", f: detect.PNG, file: "a.png", data: colorPNG("gAMA", "\x00\x01\x86\xa0"),
			tools:  []string{"truepng", "pngout", "oxipng", "pngwolf", "ect", "pingo", "deflopt", "defluff", "deflopt"},
			hasNot: []string{"--strip", "remove", "-g0"},
		},
		{
			name: "png srgb strips", f: detect.PNG, file: "a.png", data: colorPNG("sRGB", "\x00"),
			has: []string{"oxipng --zopfli --alpha --quiet -o3 --strip all {in}"},
		},
		{
			name: "png wide-gamut cicp keeps metadata", f: detect.PNG, file: "a.png", data: colorPNG("cICP", "\x09\x10\x00\x01"),
			has: []string{"pingo -s2 -nostrip -lossless {in}"}, hasNot: []string{"--strip"},
		},
		{
			name: "apng", f: detect.PNG, file: "a.png", data: apng,
			set:   func(s *config.Settings) { s.PNG.Lossy = true },
			tools: []string{"apngopt", "pngoptimizer", "oxipng", "ect", "pingo", "defluff"},
			has: []string{
				"apngopt {in} {out}",
				"oxipng --zopfli --alpha --quiet -o3 --scale16 --strip safe {in}",
				"ect -quiet --mt-deflate --allfilters --reuse -5 {in}",
			},
		},
		{
			name: "apng metadata", f: detect.PNG, file: "a.png", data: apng,
			set:   func(s *config.Settings) { s.PNG.KeepMetadata = true },
			tools: []string{"oxipng", "ect", "pingo", "defluff"},
			has:   []string{"oxipng --zopfli --alpha --quiet -o3 {in}"},
		},
		{
			name: "color-managed apng keeps chunks", f: detect.PNG, file: "a.png",
			data:   slices.Concat(apng[:33], chunk("gAMA", []byte("\x00\x01\x86\xa0")), apng[33:]),
			tools:  []string{"oxipng", "ect", "pingo", "defluff"},
			hasNot: []string{"--strip"},
		},
		{
			name: "9-patch", f: detect.PNG, file: "button.9.PNG", data: plainPNG,
			set:   func(s *config.Settings) { s.PNG.Lossy = true },
			tools: []string{"oxipng", "leanify", "pngwolf", "ect", "pingo", "defluff"},
			has:   []string{"pingo -s2 -lossless {in}"},
		},
		{name: "ico", f: detect.ICO, file: "a.ico", tools: []string{"leanify"}, has: []string{"leanify -q -p -i 6 {in}"}},
		{
			name: "ico with gamma png", f: detect.ICO, file: "a.ico", skip: "ICO with color-managed PNG",
			data: func() []byte {
				p := colorPNG("gAMA", "\x00\x01\x86\xa0")
				return slices.Concat([]byte("\x00\x00\x01\x00\x01\x00\x10\x10\x00\x00\x01\x00\x20\x00"),
					binary.LittleEndian.AppendUint32(nil, uint32(len(p))), []byte("\x16\x00\x00\x00"), p)
			}(),
		},
		{name: "ico metadata", f: detect.ICO, file: "a.ico", set: func(s *config.Settings) { s.PNG.KeepMetadata = true }, skip: nothing},
		{
			name: "gif lossy", f: detect.GIF, file: "a.gif", set: func(s *config.Settings) { s.GIF.Lossy = true },
			tools: []string{"magick", "gifsicle"},
			has: []string{
				"magick -quiet {in} -layers optimize -compress LZW -strip {out}",
				"gifsicle -w -j --no-conserve-memory -o {out} -O3 --no-comments --no-extensions --no-names --lossy=85 {in}",
			},
		},
		{name: "gif lossless", f: detect.GIF, file: "a.gif", tools: []string{"gifsicle"}},
		{
			name: "gif with color profile", f: detect.GIF, file: "a.gif", data: []byte("GIF89a\x21\xff\x0bICCRGBG1012\x02xx\x00"),
			has: []string{"gifsicle -w -j --no-conserve-memory -o {out} -O3 {in}"},
		},
		{
			name: "gif metadata", f: detect.GIF, file: "a.gif", set: func(s *config.Settings) { s.GIF.KeepMetadata = true },
			has: []string{"gifsicle -w -j --no-conserve-memory -o {out} -O3 {in}"}, hasNot: []string{"-strip", "--lossy"},
		},
		{
			name: "webp lossy level 9", f: detect.WebP, file: "a.webp", set: func(s *config.Settings) { s.Level, s.WebP.Lossy = 9, true },
			has: []string{"pingo -s4 -quality=95 {in}", "cwebp -mt -quiet -m 6 {in} -o {out}"}, hasNot: []string{"-webp"},
		},
		{
			name: "webp lossless", f: detect.WebP, file: "a.webp",
			has: []string{"pingo -s2 -lossless {in}", "cwebp -mt -quiet -z 5 -lossless {in} -o {out}"},
		},
		{
			name: "webp metadata", f: detect.WebP, file: "a.webp", set: func(s *config.Settings) { s.WebP.KeepMetadata = true },
			has: []string{"pingo -s2 -lossless -nostrip {in}", "cwebp -mt -quiet -z 5 -lossless -metadata all {in} -o {out}"},
		},
		{
			name: "rotated webp lossy", f: detect.WebP, file: "a.webp", data: rotatedWebP(), set: func(s *config.Settings) { s.WebP.Lossy = true },
			tools: []string{"cwebp"}, has: []string{"cwebp -mt -quiet -m 3 -metadata all {in} -o {out}"},
		},
		{
			name: "rotated webp", f: detect.WebP, file: "a.webp", data: rotatedWebP(),
			has: []string{"pingo -s2 -lossless -nostrip {in}", "cwebp -mt -quiet -z 5 -lossless -metadata all {in} -o {out}"},
		},
		{
			name: "color-managed webp", f: detect.WebP, file: "a.webp",
			data: webpFile(riffChunk("VP8X", "\x20"+strings.Repeat("\x00", 9)), riffChunk("ICCP", iccProfile(false)), riffChunk("VP8L", "abc")),
			has:  []string{"pingo -s2 -lossless -nostrip {in}", "cwebp -mt -quiet -z 5 -lossless -metadata all {in} -o {out}"},
		},
		{
			name: "srgb webp strips", f: detect.WebP, file: "a.webp",
			data: webpFile(riffChunk("VP8X", "\x20"+strings.Repeat("\x00", 9)), riffChunk("ICCP", iccProfile(true)), riffChunk("VP8L", "abc")),
			has:  []string{"pingo -s2 -lossless {in}", "cwebp -mt -quiet -z 5 -lossless {in} -o {out}"},
		},
		{
			name: "animated webp", f: detect.WebP, file: "a.webp", skip: "animated WebP",
			data: webpFile(riffChunk("VP8X", "\x02"+strings.Repeat("\x00", 9)), riffChunk("ANIM", "\x00\x00\x00\x00\x00\x00"), riffChunk("ANMF", "frame")),
		},
		{
			name: "bmp", f: detect.BMP, file: "a.bmp", tools: []string{"magick", "imagew"},
			has: []string{"magick -quiet {in} -compress RLE -strip {out}", "imagew -opt bmp:version=auto -noresize -zipcmprlevel 9 -outfmt bmp -compress rle {in} {out}"},
		},
		{
			name: "tiff", f: detect.TIFF, file: "a.tif", data: tiffFile(256, 257, 262),
			has: []string{"magick -quiet {in} -compress ZIP -strip {out}"}, tools: []string{"magick"},
		},
		{
			name: "tiff with color profile", f: detect.TIFF, file: "a.tif", data: tiffFile(256, 262, 34675),
			has: []string{"magick -quiet {in} -compress ZIP {out}"}, tools: []string{"magick"},
		},
		{
			name: "bmp with linked profile", f: detect.BMP, file: "a.bmp", tools: []string{"magick"},
			data: func() []byte {
				b := make([]byte, 138)
				copy(b, "BM")
				b[14] = 124
				copy(b[70:], "KNIL")
				return b
			}(),
			has: []string{"magick -quiet {in} -compress RLE {out}"},
		},
		{name: "tiff metadata", f: detect.TIFF, file: "a.tif", data: tiffFile(256), set: func(s *config.Settings) { s.TIFF.KeepMetadata = true }, skip: nothing},
		{name: "geotiff", f: detect.TIFF, file: "a.tif", data: tiffFile(256, 34735), skip: "GeoTIFF"},
		{name: "photoshop tiff", f: detect.TIFF, file: "a.tif", data: tiffFile(256, 37724), skip: "TIFF with Photoshop layers"},
		{name: "bigtiff", f: detect.TIFF, file: "a.tif", data: []byte("II+\x00\x08\x00\x00\x00"), skip: "unsupported TIFF"},
		{
			name: "svg", f: detect.SVG, file: "a.svg", data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0"/></svg>`),
			tools: []string{"leanify", "minify"}, has: []string{"minify --type svg -o {out} {in}"},
		},
		{
			name: "svg with text", f: detect.SVG, file: "a.svg", skip: "SVG with text",
			data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><text>Hello <tspan>big</tspan></text></svg>`),
		},
		{name: "svg disabled", f: detect.SVG, file: "a.svg", set: func(s *config.Settings) { s.Web.SVG = false }, skip: disabled},
		{
			name: "pdf no downsampling", f: detect.PDF, file: "a.pdf", data: []byte("%PDF-1.5 /OCG"),
			set:   func(s *config.Settings) { s.PDF.SkipLayered = true },
			tools: []string{"mutool", "cpdf", "qpdf"},
			has: []string{
				"mutool clean -g -z {in} {out}",
				"cpdf -squeeze {in} -o {out}",
				"qpdf {in} --compress-streams=y --decode-level=generalized --recompress-flate --compression-level=9 --object-streams=generate {out}",
			},
		},
		{
			name: "pdf ebook", f: detect.PDF, file: "a.pdf", data: []byte("%PDF-1.5"),
			set:   func(s *config.Settings) { s.PDF.Profile = "ebook" },
			tools: []string{"mutool", "gs", "cpdf", "qpdf"},
			has: []string{"gs -dPDFSETTINGS=/ebook -dColorImageDownsampleType=/Bicubic -dGrayImageDownsampleType=/Bicubic " +
				"-dMonoImageDownsampleType=/Bicubic -dOptimize=true -dConvertCMYKImagesToRGB=true -dColorConversionStrategy=/sRGB " +
				"-dPrinted=false -q -dBATCH -dNOPAUSE -dSAFER -dNOPROMPT -sDEVICE=pdfwrite -dDetectDuplicateImages=true " +
				"-dAutoRotatePages=/None -dCompatibilityLevel=1.4 -sOutputFile={out} {in}"},
		},
		{
			name: "pdf custom dpi", f: detect.PDF, file: "a.pdf", data: []byte("%PDF-1.5"),
			set: func(s *config.Settings) { s.PDF.Profile, s.PDF.DPI = "custom", 200 },
			has: []string{"qpdf {in} --compress-streams=y --decode-level=generalized --recompress-flate --compression-level=9 --optimize-images --object-streams=generate {out}"},
		},
		{
			name: "illustrator", f: detect.PDF, file: "a.ai", data: []byte("%PDF-1.5"),
			set: func(s *config.Settings) { s.PDF.Profile = "screen" }, tools: []string{"mutool", "cpdf", "qpdf"},
		},
		{
			name: "layered pdf", f: detect.PDF, file: "a.pdf", data: []byte("%PDF-1.5 /OCG"),
			set: func(s *config.Settings) { s.PDF.Profile, s.PDF.SkipLayered = "ebook", true }, skip: "layered PDF",
		},
		{
			name: "layered pdf allowed", f: detect.PDF, file: "a.pdf", data: []byte("%PDF-1.5 /OCG"),
			set: func(s *config.Settings) { s.PDF.Profile = "ebook" }, tools: []string{"mutool", "gs", "cpdf", "qpdf"},
		},
		{
			name: "signed pdf", f: detect.PDF, file: "a.pdf", skip: "signed PDF",
			data: []byte("%PDF-1.7\n5 0 obj << /Type /Sig /ByteRange [0 10 20 30] /Contents <00> >> endobj"),
		},
		{
			name: "pdfa", f: detect.PDF, file: "a.pdf", skip: "PDF/A document",
			data: []byte(`%PDF-1.4 <rdf:Description pdfaid:part="1" pdfaid:conformance="B"/>`),
		},
		{name: "html disabled", f: detect.HTML, file: "a.html", skip: disabled},
		{
			name: "html", f: detect.HTML, file: "a.html", set: func(s *config.Settings) { s.Web.HTML = true },
			data:  []byte("<pre>  a\n    b</pre><textarea> x </textarea>"),
			tools: []string{"minify"}, has: []string{"minify --type html -o {out} {in}"},
		},
		{
			name: "html white-space pre", f: detect.HTML, file: "a.html", set: func(s *config.Settings) { s.Web.HTML = true },
			data: []byte(`<style>.code { White-Space : Pre-Wrap }</style>`), skip: "HTML with whitespace-sensitive content",
		},
		{
			name: "html xmp", f: detect.HTML, file: "a.html", set: func(s *config.Settings) { s.Web.HTML = true },
			data: []byte("<XMP>a    b</XMP>"), skip: "HTML with whitespace-sensitive content",
		},
		{
			name: "html svg text", f: detect.HTML, file: "a.html", set: func(s *config.Settings) { s.Web.HTML = true },
			data: []byte(`<svg><text x="1">Hello <tspan>big</tspan></text></svg>`), skip: "HTML with whitespace-sensitive content",
		},
		{name: "css disabled", f: detect.CSS, file: "a.css", skip: disabled},
		{
			name: "css", f: detect.CSS, file: "a.css", set: func(s *config.Settings) { s.Web.CSS = true },
			tools: []string{"minify"}, has: []string{"minify --type css -o {out} {in}"},
		},
		{name: "js disabled", f: detect.JS, file: "a.js", skip: disabled},
		{
			name: "js extra extension", f: detect.JS, file: "a.ES6", set: func(s *config.Settings) { s.Web.JSExtensions = []string{"es6"} },
			tools: []string{"minify"}, has: []string{"minify --type js -o {out} {in}"},
		},
		{
			name: "json", f: detect.JS, file: "a.json", set: func(s *config.Settings) { s.Web.JS = true },
			tools: []string{"minify"}, has: []string{"minify --type json --json-keep-numbers -o {out} {in}"},
		},
		{name: "xml", f: "xml", file: "a.xml", skip: "unsupported format"},
		{
			name: "zip", f: detect.ZIP, file: "a.docx", data: zipFile(nil),
			tools: []string{"leanify", "ect", "advzip", "deflopt", "defluff", "deflopt"},
			has: []string{
				"leanify -q -p -i 6 --zip-deflate -d 1 {in}",
				"ect -quiet --mt-deflate -zip -strip --disable-jpg --disable-png -5 {in}",
				"advzip -z -q -4 -i 6 {in}",
				"deflopt /a /b /s {in}",
			},
		},
		{
			name: "zip recurse", f: detect.ZIP, file: "a.zip",
			set: func(s *config.Settings) { s.Archive.Recurse, s.JPEG.KeepMetadata = true, true },
			has: []string{
				"leanify -q -p -i 6 --zip-deflate --keep-exif --keep-icc --jpeg-keep-all {in}",
				"ect -quiet --mt-deflate -zip -strip --disable-jpg -5 {in}",
			},
		},
		{
			name: "zip metadata", f: detect.ZIP, file: "a.zip",
			set: func(s *config.Settings) {
				s.Archive.Recurse, s.Archive.KeepMetadata = true, true
				s.JPEG.KeepMetadata, s.JPEG.Arithmetic = true, true
			},
			data:  zipFile(nil),
			tools: []string{"deflopt", "defluff", "deflopt"},
			has:   []string{"deflopt /a /b /s /c /k {in}"},
		},
		{
			name: "zip directories", f: detect.ZIP, file: "a.zip", data: zipFile(nil, "empty/"),
			tools: []string{"leanify", "ect", "advzip", "defluff"},
		},
		{name: "zip unicode names", f: detect.ZIP, file: "a.zip", data: zipFile([]byte{0x75, 0x70, 2, 0, 1, 0}), skip: "ZIP with Unicode name fields"},
		{
			name: "zip timestamps", f: detect.ZIP, file: "a.zip", data: zipFile([]byte{0x55, 0x54, 1, 0, 0}),
			tools: []string{"leanify", "ect", "advzip", "deflopt", "defluff", "deflopt"},
		},
		{
			name: "gz", f: detect.GZ, file: "a.tgz", data: gz("a"),
			tools: []string{"gzip", "leanify", "advdef", "zrecompress", "ect", "deflopt", "defluff", "deflopt"},
			has: []string{
				"gzip <func>",
				"leanify -q -p -i 6 -d 1 {in}",
				"zrecompress -tgz {in}",
				"ect -quiet --mt-deflate --allfilters -gzip -strip -5 {in}",
			},
		},
		{
			name: "gz recurse", f: detect.GZ, file: "a.svgz", data: gz("a"), set: func(s *config.Settings) { s.Archive.Recurse = true },
			has: []string{"leanify -q -p -i 6 --keep-exif {in}"},
		},
		{name: "gz multi-member", f: detect.GZ, file: "a.gz", data: append(gz("a"), gz("b")...), skip: "multi-member gzip"},
		{name: "gz trailing data", f: detect.GZ, file: "a.gz", data: append(gz("a"), 0), skip: "multi-member gzip"},
		{name: "gz damaged", f: detect.GZ, file: "a.gz", data: gz("a")[:20], skip: "damaged gzip"},
		{
			name: "gz metadata", f: detect.GZ, file: "a.gz", data: gz("a"), set: func(s *config.Settings) { s.Archive.KeepMetadata = true },
			tools:  []string{"advdef", "deflopt", "defluff", "deflopt"},
			has:    []string{"deflopt /a /b /s /c /k {in}"},
			hasNot: []string{"-strip"},
		},
		{name: "tar", f: detect.TAR, file: "a.tar", skip: nothing},
		{
			name: "tar recurse", f: detect.TAR, file: "a.tar", set: func(s *config.Settings) { s.Archive.Recurse = true },
			has: []string{"leanify -q -p --keep-exif -i 6 {in}"},
		},
		{name: "7z fast", f: detect.SevenZ, file: "a.7z", skip: nothing},
		{
			name: "7z level 8", f: detect.SevenZ, file: "a.7z", set: func(s *config.Settings) { s.Level = 8 },
			tools: []string{"m7zrepacker"}, has: []string{"m7zrepacker <func>"},
		},
		{name: "sqlite", f: detect.SQLite, file: "a.db", tools: []string{"sqlite3"}, has: []string{"sqlite3 <func>"}},
		{name: "sqlite with wal", f: detect.SQLite, file: "a.db", sidecars: []string{"-wal"}, skip: "database in use"},
		{name: "sqlite with shm", f: detect.SQLite, file: "a.db", sidecars: []string{"-shm"}, skip: "database in use"},
		{name: "sqlite with journal", f: detect.SQLite, file: "a.db", sidecars: []string{"-journal"}, skip: "database in use"},
		{
			name: "flac level 9", f: detect.FLAC, file: "a.flac", set: func(s *config.Settings) { s.Level = 9 },
			tools: []string{"flac", "flacout"},
			has:   []string{"flac --force -s -8 --best -ep {in}", "flacout /q /y {in} {out}"},
		},
		{
			name: "flac trim", f: detect.FLAC, file: "a.flac", set: func(s *config.Settings) { s.Audio.TrimSilence = true },
			tools: []string{"shntool", "flac"}, has: []string{"flac --force -s -8 --best -e {in}"},
		},
		{
			name: "flac metadata level 9", f: detect.FLAC, file: "a.flac",
			set:   func(s *config.Settings) { s.Level, s.Audio.KeepMetadata, s.Audio.TrimSilence = 9, true, true },
			tools: []string{"flac"}, hasNot: []string{"--keep-foreign-metadata"},
		},
		{name: "wav", f: detect.WAV, file: "a.wav", tools: []string{"shntool"}},
		{name: "wav trim", f: detect.WAV, file: "a.wav", set: func(s *config.Settings) { s.Audio.TrimSilence = true }, tools: []string{"shntool", "shntool"}},
		{name: "wav metadata", f: detect.WAV, file: "a.wav", set: func(s *config.Settings) { s.Audio.KeepMetadata = true }, skip: nothing},
		{
			name: "vorbis", f: detect.OGG, file: "a.ogg", data: ogg("\x01vorbis"), tools: []string{"optivorbis"},
			has: []string{"optivorbis --vendor_string_action empty --comment_fields_action delete {in} {out}"},
		},
		{
			name: "vorbis metadata", f: detect.OGG, file: "a.oga", data: ogg("\x01vorbis"), set: func(s *config.Settings) { s.Audio.KeepMetadata = true },
			has: []string{"optivorbis --vendor_string_action copy --comment_fields_action copy {in} {out}"},
		},
		{
			name: "theora named ogg", f: detect.OGG, file: "a.ogg", data: ogg("\x80theora", "\x01vorbis"),
			tools: []string{"ffmpeg", "rehuff_theora"},
			has:   []string{"ffmpeg -y -v error -xerror -i {in} -c copy -map 0 -map_metadata:g -1 {out}", "rehuff_theora {in} {out}"},
		},
		{name: "theora", f: detect.OGG, file: "a.ogv", data: ogg("\x80theora"), tools: []string{"ffmpeg", "rehuff_theora"}},
		{name: "opus", f: detect.OGG, file: "a.opus", data: ogg("OpusHead"), skip: "unsupported Ogg stream (opus)"},
		{name: "vorbis named ogv", f: detect.OGG, file: "a.ogv", data: ogg("\x01vorbis"), tools: []string{"optivorbis"}},
		{
			name: "ogg mixed", f: detect.OGG, file: "a.ogg", data: ogg("\x80theora", "\x7fFLAC", "\x80kate", "\x7fFLAC"),
			skip: "unsupported Ogg stream (flac, unknown)",
		},
		{name: "ogg damaged", f: detect.OGG, file: "a.ogg", data: []byte("OggS"), skip: "unsupported Ogg stream (unknown)"},
		{
			name: "mp4", f: detect.MP4, file: "a.mp4", tools: []string{"ffmpeg", "mp4file"},
			has: []string{"ffmpeg -y -v error -xerror -i {in} -c copy -map 0 -map_metadata:g -1 {out}", "mp4file --optimize -q {in}"},
		},
		{
			name: "fragmented mp4", f: detect.MP4, file: "a.mp4", tools: []string{"ffmpeg"},
			data: []byte(box("ftyp", "isom") + box("moov", box("mvhd")) + box("moof", box("mfhd")) + box("mdat", "x")),
		},
		{
			name: "mp4 metadata", f: detect.MP4, file: "a.mov", set: func(s *config.Settings) { s.Video.KeepMetadata = true },
			has: []string{"ffmpeg -y -v error -xerror -i {in} -c copy -map 0 {out}"},
		},
		{
			name: "m4a uses audio metadata", f: detect.MP4, file: "a.m4a", set: func(s *config.Settings) { s.Video.KeepMetadata = true },
			has: []string{"ffmpeg -y -v error -xerror -i {in} -c copy -map 0 -map_metadata:g -1 {out}"},
		},
		{
			name: "mka metadata", f: detect.MKV, file: "a.mka", data: mkv, set: func(s *config.Settings) { s.Audio.KeepMetadata = true },
			has: []string{"ffmpeg -y -v error -xerror -i {in} -c copy -map 0 {out}"},
		},
		{
			name: "mkv", f: detect.MKV, file: "a.mkv", data: mkv, tools: []string{"ffmpeg", "mkclean"},
			has: []string{"mkclean --optimize --unsafe --quiet {in} {out}"},
		},
		{
			name: "mkv hdr", f: detect.MKV, file: "a.mkv", tools: []string{"ffmpeg"},
			data: mkvFile(ebml(0x86, "V_MPEGH/ISO/HEVC"), ebml(0xe0, ebml(0x55b0, ebml(0x55ba, "\x10")))),
		},
		{name: "mka opus", f: detect.MKV, file: "a.mka", data: mkvFile(ebml(0x86, "A_OPUS"), ebml(0x56aa, "\x01")), tools: []string{"ffmpeg"}},
		{name: "webm", f: detect.MKV, file: "a.webm", data: mkv, tools: []string{"ffmpeg"}},
		{name: "exe disabled", f: detect.EXE, file: "a.exe", data: peFile(false, "", 0), skip: disabled},
		{
			name: "exe", f: detect.EXE, file: "a.exe", data: peFile(false, "", 0),
			set:   func(s *config.Settings) { s.Exe.Enabled = true },
			tools: []string{"leanify", "petrim", "strip"},
			has:   []string{"petrim {in}", "strip --strip-all -o {out} {in}"},
		},
		{
			name: "aslr exe skips leanify", f: detect.EXE, file: "a.exe", data: peFile(false, "", pe.IMAGE_DLLCHARACTERISTICS_DYNAMIC_BASE),
			set:   func(s *config.Settings) { s.Exe.Enabled = true },
			tools: []string{"petrim", "strip"},
		},
		{
			name: "exe upx level 9", f: detect.EXE, file: "a.exe", data: peFile(false, "", 0),
			set:   func(s *config.Settings) { s.Level, s.Exe.Enabled, s.Exe.UPX, s.Exe.PETrim = 9, true, true, false },
			tools: []string{"leanify", "strip", "upx"},
			has:   []string{"upx --no-backup --force -9 --best --lzma --ultra-brute {in}"},
		},
		{
			name: "signed exe", f: detect.EXE, file: "a.exe", data: peFile(true, "", 0),
			set: func(s *config.Settings) { s.Exe.Enabled = true }, skip: "signed executable",
		},
		{
			name: "installer", f: detect.EXE, file: "setup.exe", data: peFile(false, "Inno Setup Setup Data", 0),
			set: func(s *config.Settings) { s.Exe.Enabled = true }, skip: "installer/self-extracting",
		},
		{
			name: "dll", f: detect.DLL, file: "a.dll", data: peFile(false, "", 0),
			set: func(s *config.Settings) { s.Exe.Enabled = true }, tools: []string{"petrim", "strip"},
		},
		{
			name: "signed dll", f: detect.DLL, file: "a.dll", data: peFile(true, "", 0),
			set: func(s *config.Settings) { s.Exe.Enabled = true }, skip: "signed executable",
		},
		{name: "unknown", f: "pcx", file: "a.pcx", skip: "unsupported format"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.file)
			if err := os.WriteFile(path, tc.data, 0o644); err != nil {
				t.Fatal(err)
			}
			for _, s := range tc.sidecars {
				if err := os.WriteFile(path+s, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			s := config.Default()
			if tc.set != nil {
				tc.set(s)
			}
			steps, skip := For(tc.f, path, s)
			if skip != tc.skip {
				t.Fatalf("skip = %q, want %q", skip, tc.skip)
			}
			if skip != "" && steps != nil {
				t.Errorf("steps returned with skip reason")
			}
			got := render(steps)
			if tc.tools != nil {
				var tools []string
				for _, s := range steps {
					tools = append(tools, s.Tool)
				}
				if !slices.Equal(tools, tc.tools) && !(len(tools) == 0 && len(tc.tools) == 0) {
					t.Errorf("tools = %v, want %v", tools, tc.tools)
				}
			}
			for _, h := range tc.has {
				if !slices.Contains(got, h) {
					t.Errorf("missing step %q in:\n%s", h, strings.Join(got, "\n"))
				}
			}
			for _, n := range tc.hasNot {
				for _, g := range got {
					if strings.Contains(g, n) {
						t.Errorf("step %q contains %q", g, n)
					}
				}
			}
			for _, st := range steps {
				if st.Tool == "gs" && (st.Accept == nil || st.Accept(30000, 2000) || !st.Accept(30000, 5000)) {
					t.Errorf("gs Accept must reject tiny results")
				}
				if slices.Contains(remuxTools, st.Tool) && (st.Accept == nil || st.Accept(1000, 499) || !st.Accept(1000, 500)) {
					t.Errorf("%s Accept must reject results below half the size", st.Tool)
				}
			}
		})
	}
}

// Lossy steps must only appear when a lossy setting asks for them.
func TestDefaultsAreLossless(t *testing.T) {
	lossy := []string{"pngquant", "cjpegli", "guetzli", "jpeg-recompress", "magick -quiet {in} -interlace", "-quality=",
		"-layers optimize", "--lossy", "gs ", "--optimize-images", "--scale16", " -l ", "-g1", "-m ", "tinydng", "jsmin", "tidy"}
	dir := t.TempDir()
	files := map[detect.Format][]byte{
		detect.JPEG: jpegFile(3), detect.PNG: pngFile(false), detect.TIFF: tiffFile(256),
		detect.PDF: []byte("%PDF-1.5"), detect.OGG: ogg("\x80theora", "\x01vorbis"), detect.MKV: mkvFile(),
	}
	s := config.Default()
	s.Web.HTML, s.Web.CSS, s.Web.JS, s.Exe.Enabled, s.Archive.Recurse, s.Level = true, true, true, true, true, 9
	for f := range builders {
		p := filepath.Join(dir, string(f))
		if err := os.WriteFile(p, files[f], 0o644); err != nil {
			t.Fatal(err)
		}
		steps, _ := For(f, p, s)
		for _, r := range render(steps) {
			for _, l := range lossy {
				if strings.Contains(r+" ", l) {
					t.Errorf("%s: lossy step %q (%q)", f, r, l)
				}
			}
		}
	}
}

func TestPEOverlaySkipped(t *testing.T) {
	exe, err := filepath.Abs(filepath.Join(plugins, "jhead.exe"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Skip("reference tools not present")
	}
	s := config.Default()
	s.Exe.Enabled = true
	if _, skip := For(detect.EXE, exe, s); skip != "" {
		t.Fatalf("plain exe skipped: %s", skip)
	}
	p := filepath.Join(t.TempDir(), "app.exe")
	if err := os.WriteFile(p, append(b, "PyInstaller payload MEI\x0c\x0b\x0a\x0b\x0e"...), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []detect.Format{detect.EXE, detect.DLL} {
		if _, skip := For(f, p, s); skip != overlay {
			t.Errorf("%s: skip = %q, want %q", f, skip, overlay)
		}
	}
}

func TestMP3Args(t *testing.T) {
	steps, _ := For(detect.MP3, "a.mp3", config.Default())
	// -a takes an empty argument, which add would drop.
	want := []string{"-t", "-s", "-z", "-a", "", "-A", "-f", "{in}", "{out}"}
	if len(steps) != 1 || !slices.Equal(steps[0].Args, want) || steps[0].Func == nil {
		t.Fatalf("got %q, want %q", steps[0].Args, want)
	}
	s := config.Default()
	s.Audio.KeepMetadata = true
	if steps, _ := For(detect.MP3, "a.mp3", s); slices.Contains(steps[0].Args, "-t") {
		t.Fatalf("metadata kept but -t passed: %q", steps[0].Args)
	}
}

func TestEveryFormat(t *testing.T) {
	formats := []detect.Format{detect.JPEG, detect.PNG, detect.ICO, detect.GIF, detect.WebP, detect.BMP, detect.TIFF,
		detect.SVG, detect.PDF, detect.HTML, detect.CSS, detect.JS, detect.ZIP, detect.GZ, detect.TAR,
		detect.SevenZ, detect.SQLite, detect.FLAC, detect.WAV, detect.MP3, detect.OGG, detect.MP4, detect.MKV,
		detect.EXE, detect.DLL}
	for _, f := range formats {
		if _, ok := builders[f]; !ok {
			t.Errorf("no pipeline for %s", f)
		}
	}
}
