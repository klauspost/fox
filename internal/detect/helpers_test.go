package detect

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exif returns an APP1 segment whose IFD0 holds the given tags (SHORT values).
func exif(bo binary.AppendByteOrder, tags ...uint16) string {
	return app("\xe1", "Exif\x00\x00"+exifTIFF(bo, tags...))
}

func exifTIFF(bo binary.AppendByteOrder, tags ...uint16) string {
	t := []byte("MM\x00*")
	if bo == binary.LittleEndian {
		t = []byte("II*\x00")
	}
	t = bo.AppendUint32(t, 8)
	t = bo.AppendUint16(t, uint16(len(tags)/2))
	for i := 0; i+1 < len(tags); i += 2 {
		t = bo.AppendUint16(bo.AppendUint16(t, tags[i]), 3)
		t = bo.AppendUint32(t, 1)
		t = append(bo.AppendUint16(t, tags[i+1]), 0, 0)
	}
	return string(bo.AppendUint32(t, 0))
}

func app(marker, data string) string {
	return "\xff" + marker + string(binary.BigEndian.AppendUint16(nil, uint16(2+len(data)))) + data
}

func pngChunk(typ, data string) string {
	return string(binary.BigEndian.AppendUint32(nil, uint32(len(data)))) + typ + data + "CRC!"
}

func riffChunk(typ, data string) string {
	if len(data)%2 == 1 {
		return typ + string(binary.LittleEndian.AppendUint32(nil, uint32(len(data)))) + data + "\x00"
	}
	return typ + string(binary.LittleEndian.AppendUint32(nil, uint32(len(data)))) + data
}

func TestOrientation(t *testing.T) {
	png := func(chunks ...string) string {
		return "\x89PNG\r\n\x1a\n" + pngChunk("IHDR", strings.Repeat("\x00", 13)) + strings.Join(chunks, "") + pngChunk("IEND", "")
	}
	webp := func(chunks ...string) string {
		body := "WEBP" + riffChunk("VP8X", "\x08"+strings.Repeat("\x00", 9)) + strings.Join(chunks, "")
		return "RIFF" + string(binary.LittleEndian.AppendUint32(nil, uint32(len(body)))) + body
	}
	idat, vp8l := pngChunk("IDAT", "xx"), riffChunk("VP8L", "abc")
	xmp := app("\xe1", "http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta/>")
	tests := []struct {
		name, content string
		want          int
	}{
		{"big endian", jpeg(3, exif(binary.BigEndian, 0x010f, 1, 0x0112, 6)), 6},
		{"little endian", jpeg(3, exif(binary.LittleEndian, 0x0112, 8)), 8},
		{"after xmp", jpeg(3, xmp+exif(binary.LittleEndian, 0x0112, 3)), 3},
		{"normal", jpeg(3, exif(binary.BigEndian, 0x0112, 1)), 1},
		{"no orientation", jpeg(3, exif(binary.BigEndian, 0x010f, 1)), 0},
		{"no exif", jpeg(3, ""), 0},
		{"bad ifd offset", jpeg(3, app("\xe1", "Exif\x00\x00MM\x00*\xff\xff\xff\xf0")), 0},
		{"entry count past end", jpeg(3, app("\xe1", "Exif\x00\x00II*\x00\x08\x00\x00\x00\xff\xff\x12\x01")), 0},
		{"short segment", jpeg(3, "\xff\xe1\x00\x01"), 0},
		{"truncated", jpeg(3, exif(binary.BigEndian, 0x0112, 6))[:20], 0},
		{"png eXIf", png(pngChunk("eXIf", exifTIFF(binary.BigEndian, 0x0112, 6)), idat), 6},
		{"png eXIf with Exif header", png(pngChunk("eXIf", "Exif\x00\x00"+exifTIFF(binary.LittleEndian, 0x0112, 5)), idat), 5},
		{"png eXIf after IDAT", png(idat, pngChunk("eXIf", exifTIFF(binary.LittleEndian, 0x0112, 8))), 8},
		{"png without eXIf", png(idat), 0},
		{"png truncated", png(idat)[:30], 0},
		{"webp EXIF", webp(vp8l, riffChunk("EXIF", exifTIFF(binary.LittleEndian, 0x0112, 6))), 6},
		{"webp EXIF with Exif header", webp(vp8l, riffChunk("EXIF", "Exif\x00\x00"+exifTIFF(binary.BigEndian, 0x0112, 7))), 7},
		{"webp without EXIF", webp(vp8l), 0},
		{"webp huge chunk", webp(vp8l)[:12] + "VP8L\xff\xff\xff\xff", 0},
		{"unknown", "GIF89a", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Orientation(write(t, "a.img", tt.content)); got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func be32(v int) string { return string(binary.BigEndian.AppendUint32(nil, uint32(v))) }

func s15(f float64) string { return be32(int(math.Round(f * 65536))) }

var (
	adobeD50 = [3][3]float64{{0.6097, 0.3111, 0.0195}, {0.2053, 0.6257, 0.0609}, {0.1492, 0.0632, 0.7446}}
	p3D50    = [3][3]float64{{0.5151, 0.2412, -0.0011}, {0.2920, 0.6922, 0.0419}, {0.1571, 0.0666, 0.7841}}
	// v2 profiles sample the sRGB curve, v4 profiles describe it with a parametric curve.
	srgbCurv = func() string {
		s := "curv\x00\x00\x00\x00" + be32(1024)
		for i := range 1024 {
			x := float64(i) / 1023
			y := x / 12.92
			if x > 0.04045 {
				y = math.Pow((x+0.055)/1.055, 2.4)
			}
			s += string(binary.BigEndian.AppendUint16(nil, uint16(math.Round(y*65535))))
		}
		return s
	}()
	srgbPara = "para\x00\x00\x00\x00\x00\x03\x00\x00" + s15(2.4) + s15(1/1.055) + s15(0.055/1.055) + s15(1/12.92) + s15(0.04045)
	linear   = "curv\x00\x00\x00\x00" + be32(0)
	gamma22  = "curv\x00\x00\x00\x00" + be32(1) + "\x02\x33\x00\x00"
	gamma18  = "curv\x00\x00\x00\x00" + be32(1) + "\x01\xcd\x00\x00"
)

// iccProfile builds an RGB matrix/TRC ICC profile with the given D50 colorants and one tone curve for all
// channels. Extra tags get dummy data.
func iccProfile(colorants [3][3]float64, trc string, extra ...string) string {
	type tag struct{ sig, data string }
	tags := []tag{{"desc", "desc\x00\x00\x00\x00" + be32(5) + "test\x00"}}
	for i, c := range []string{"rXYZ", "gXYZ", "bXYZ"} {
		tags = append(tags, tag{c, "XYZ \x00\x00\x00\x00" + s15(colorants[i][0]) + s15(colorants[i][1]) + s15(colorants[i][2])})
	}
	for _, c := range []string{"rTRC", "gTRC", "bTRC"} {
		tags = append(tags, tag{c, trc})
	}
	for _, e := range extra {
		tags = append(tags, tag{e, "mft2\x00\x00\x00\x00"})
	}
	table, data := be32(len(tags)), ""
	for _, t := range tags {
		table += t.sig + be32(128+4+12*len(tags)+len(data)) + be32(len(t.data))
		data += t.data
	}
	h := be32(0) + "none\x02\x10\x00\x00mntrRGB XYZ " + strings.Repeat("\x00", 104)
	return h + table + data
}

func zlibString(s string) string {
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.String()
}

func webpFile(chunks ...string) string {
	body := "WEBP" + strings.Join(chunks, "")
	return "RIFF" + string(binary.LittleEndian.AppendUint32(nil, uint32(len(body)))) + body
}

// tiffICC is a TIFF whose first IFD holds only an InterColorProfile tag.
func tiffICC(bo interface {
	binary.ByteOrder
	binary.AppendByteOrder
}, profile string) string {
	b := []byte("II*\x00")
	if bo == binary.BigEndian {
		b = []byte("MM\x00*")
	}
	b = bo.AppendUint16(bo.AppendUint32(b, 8), 1)
	b = bo.AppendUint32(bo.AppendUint16(bo.AppendUint16(b, 34675), 7), uint32(len(profile)))
	b = bo.AppendUint32(bo.AppendUint32(b, 26), 0)
	return string(b) + profile
}

// bmpV5 is a BMP with a BITMAPV5HEADER of the given color space type, followed by profile.
// calibration sets the first endpoint value.
func bmpV5(csType, profile string, calibration ...byte) string {
	b := make([]byte, 138)
	copy(b, "BM")
	b[14] = 124
	copy(b[70:], csType)
	if len(calibration) > 0 {
		b[74] = calibration[0]
	}
	binary.LittleEndian.PutUint32(b[126:], 124)
	binary.LittleEndian.PutUint32(b[130:], uint32(len(profile)))
	return string(b) + profile
}

// gifICC is a GIF header followed by an ICC profile application extension.
func gifICC(profile string) string {
	s := "GIF89a\x01\x00\x01\x00\x00\x00\x00\x21\xff\x0bICCRGBG1012"
	for ; len(profile) > 255; profile = profile[255:] {
		s += "\xff" + profile[:255]
	}
	return s + string(byte(len(profile))) + profile + "\x00\x2c"
}

// ico builds an icon with one directory entry per image, stored in order after the directory.
func ico(images ...string) string {
	dir, data := "\x00\x00\x01\x00"+string(binary.LittleEndian.AppendUint16(nil, uint16(len(images)))), ""
	for _, img := range images {
		off := 6 + 16*len(images) + len(data)
		dir += "\x10\x10\x00\x00\x01\x00\x20\x00" + string(binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint32(nil, uint32(len(img))), uint32(off)))
		data += img
	}
	return dir + data
}

func TestColorManaged(t *testing.T) {
	png := func(chunks ...string) string {
		return pngSig + pngChunk("IHDR", strings.Repeat("\x00", 13)) + strings.Join(chunks, "") + pngChunk("IEND", "")
	}
	iccp := func(p string) string { return pngChunk("iCCP", "ICC Profile\x00\x00"+zlibString(p)) }
	app2 := func(seq, n byte, p string) string { return app("\xe2", "ICC_PROFILE\x00"+string([]byte{seq, n})+p) }
	srgb2, srgb4 := iccProfile(srgbD50, srgbCurv), iccProfile(srgbD50, srgbPara)
	adobe, p3 := iccProfile(adobeD50, gamma22), iccProfile(p3D50, srgbPara)
	srgbCHRM := pngChunk("cHRM", "\x00\x00\x7a\x26\x00\x00\x80\x84\x00\x00\xfa\x00\x00\x00\x80\xe8\x00\x00\x75\x30\x00\x00\xea\x60\x00\x00\x3a\x98\x00\x00\x17\x70")
	p3CHRM := pngChunk("cHRM", "\x00\x00\x7a\x26\x00\x00\x80\x84\x00\x01\x0c\x8e\x00\x00\x51\x45\x00\x00\x6b\x6c\x00\x00\xbc\x7e\x00\x00\x3a\x98\x00\x00\x0f\xa0")
	gama, idat := pngChunk("gAMA", "\x00\x01\x86\xa0"), pngChunk("IDAT", "xx")
	tests := []struct {
		name, content string
		want          bool
	}{
		{"jpeg plain", jpeg(3, ""), false},
		{"jpeg srgb v2", jpeg(3, app2(1, 1, srgb2)), false},
		{"jpeg srgb v4", jpeg(3, app2(1, 1, srgb4)), false},
		{"jpeg adobe rgb", jpeg(3, app2(1, 1, adobe)), true},
		{"jpeg display p3", jpeg(3, exif(binary.BigEndian, 0x0112, 1)+app2(1, 1, p3)), true},
		{"jpeg split profile", jpeg(3, app2(1, 2, p3[:140])+app2(2, 2, p3[140:])), true},
		{"jpeg split srgb", jpeg(3, app2(1, 2, srgb2[:150])+app2(2, 2, srgb2[150:])), false},
		{"jpeg short app2", jpeg(3, app("\xe2", "ICC_PROFILE\x00")), true},
		{"jpeg other app2", jpeg(3, app("\xe2", "FPXR\x00junk")), false},
		{"jpeg truncated srgb", jpeg(3, app2(1, 1, srgb4[:150])), true},
		{"jpeg linear srgb", jpeg(3, app2(1, 1, iccProfile(srgbD50, linear))), true},
		{"png plain", png(idat), false},
		{"png magick chrm", png(srgbCHRM, idat), false},
		{"png p3 chrm", png(p3CHRM, idat), true},
		{"png short chrm", png(pngChunk("cHRM", "\x00\x00\x7a\x26"), idat), true},
		{"png gama", png(gama, idat), true},
		{"png srgb gama chrm", png(pngChunk("sRGB", "\x00"), gama, p3CHRM, idat), false},
		{"png cicp", png(pngChunk("cICP", "\x09\x10\x00\x01"), pngChunk("sRGB", "\x00"), idat), true},
		{"png srgb iccp", png(iccp(srgb2), gama, idat), false},
		{"png adobe iccp", png(iccp(adobe), idat), true},
		{"png broken iccp", png(pngChunk("iCCP", "name\x00\x00garbage"), idat), true},
		{"png iccp after idat", png(idat, iccp(adobe)), false},
		{"png truncated", png(iccp(adobe))[:40], false},
		{"webp plain", webpFile(riffChunk("VP8L", "abc")), false},
		{"webp srgb", webpFile(riffChunk("VP8X", "\x20"+strings.Repeat("\x00", 9)), riffChunk("ICCP", srgb4), riffChunk("VP8L", "abc")), false},
		{"webp p3", webpFile(riffChunk("VP8X", "\x20"+strings.Repeat("\x00", 9)), riffChunk("ICCP", p3), riffChunk("VP8L", "abc")), true},
		{"webp huge chunk", webpFile("ICCP\xff\xff\xff\xff"), true},
		{"tiff adobe", tiffICC(binary.LittleEndian, adobe), true},
		{"tiff srgb", tiffICC(binary.BigEndian, srgb4), false},
		{"tiff plain", tiff(binary.LittleEndian, 2), false},
		{"bmp adobe", bmpV5("DEBM", adobe), true},
		{"bmp embedded srgb", bmpV5("DEBM", srgb2), false},
		{"bmp srgb", bmpV5("BGRs", ""), false},
		{"bmp linked", bmpV5("KNIL", ""), true},
		{"bmp calibrated", bmpV5("\x00\x00\x00\x00", "", 1), true},
		{"bmp zero calibration", bmpV5("\x00\x00\x00\x00", ""), false},
		{"bmp truncated", bmpV5("DEBM", "")[:130], true},
		{"ico gama png", ico(png(idat), png(gama, idat)), true},
		{"ico plain png", ico(png(idat)), false},
		{"ico bmp", ico("\x28\x00\x00\x00"), false},
		{"ico entry past end", "\x00\x00\x01\x00\x02\x00" + strings.Repeat("\xff", 16), false},
		{"gif", "GIF89a", false},
		{"gif adobe", gifICC(adobe), true},
		{"gif srgb", gifICC(srgb2), false},
		{"gif truncated profile", gifICC(adobe)[:300], true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ColorManaged(write(t, "a.img", tt.content)); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestICCSRGB(t *testing.T) {
	tests := []struct {
		name, profile string
		want          bool
	}{
		{"v2 sampled curve", iccProfile(srgbD50, srgbCurv), true},
		{"v4 parametric curve", iccProfile(srgbD50, srgbPara), true},
		// Named "sRGB" by GIMP, Krita and others; stripping them changes the displayed tones.
		{"linear", iccProfile(srgbD50, linear), false},
		{"gamma 1.8", iccProfile(srgbD50, gamma18), false},
		{"gamma 2.2", iccProfile(srgbD50, gamma22), false},
		{"lookup table", iccProfile(srgbD50, srgbCurv, "A2B0"), false},
		{"adobe rgb", iccProfile(adobeD50, gamma22), false},
		{"display p3", iccProfile(p3D50, srgbPara), false},
		{"gray", strings.Replace(iccProfile(srgbD50, srgbPara), "RGB ", "GRAY", 1), false},
		{"para type 4", iccProfile(srgbD50, "para\x00\x00\x00\x00\x00\x04\x00\x00"+s15(2.4)+s15(1/1.055)+s15(0.055/1.055)+s15(1/12.92)+s15(0.04045)+s15(0)+s15(0)), true},
		{"para type 0", iccProfile(srgbD50, "para\x00\x00\x00\x00\x00\x00\x00\x00"+s15(2.2)), false},
		{"short para", iccProfile(srgbD50, "para\x00\x00\x00\x00\x00\x03\x00\x00"+s15(2.4)), false},
		{"bad para type", iccProfile(srgbD50, "para\x00\x00\x00\x00\x00\x07\x00\x00"+strings.Repeat("\x00", 40)), false},
		{"short curv table", iccProfile(srgbD50, "curv\x00\x00\x00\x00"+be32(1024)+"\x00\x00"), false},
		{"nan curve", iccProfile(srgbD50, "para\x00\x00\x00\x00\x00\x01\x00\x00"+s15(0.5)+s15(-1)+s15(0)), false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := iccSRGB([]byte(tt.profile)); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
	// Real profiles shipped with Windows.
	dir := filepath.Join(os.Getenv("SystemRoot"), `System32\spool\drivers\color`)
	for name, want := range map[string]bool{"sRGB Color Space Profile.icm": true, "AdobeRGB1998.icc": false, "WideGamutRGB.icc": false, "AppleRGB.icc": false, "USWebCoatedSWOP.icc": false} {
		if p, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			if got := iccSRGB(p); got != want {
				t.Errorf("%s: got %v, want %v", name, got, want)
			}
		}
	}
}

func FuzzICCSRGB(f *testing.F) {
	for _, trc := range []string{srgbCurv, srgbPara, linear, gamma18} {
		f.Add([]byte(iccProfile(srgbD50, trc)))
	}
	f.Fuzz(func(t *testing.T, b []byte) { iccSRGB(b) })
}

func TestHasGainMap(t *testing.T) {
	gain := jpeg(3, "")
	tests := []struct {
		name, content string
		want          bool
	}{
		{"ultra hdr", jpeg(3, app("\xe1", `http://ns.adobe.com/xap/1.0/`+"\x00"+`<rdf:Description hdrgm:Version="1.0"/>`)) + "\xff\xd9" + gain, true},
		{"iso 21496-1", jpeg(3, app("\xe2", "urn:iso:std:iso:ts:21496:-1\x00\x00\x00")) + "\xff\xd9" + gain, true},
		{"apple", jpeg(3, app("\xe2", "MPF\x00")) + "\xff\xd9" + jpeg(3, app("\xe1", "http://ns.adobe.com/xap/1.0/\x00xmlns:HDRGainMap=\"http://ns.apple.com/HDRGainMap/1.0/\"")), true},
		{"camera preview", jpeg(3, app("\xe2", "MPF\x00")) + "\xff\xd9" + gain, false},
		{"plain", jpeg(3, ""), false},
		{"png", pngSig + "hdrgm", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasGainMap(write(t, "a.jpg", tt.content)); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsAnimatedWebP(t *testing.T) {
	vp8x := func(flags byte) string { return riffChunk("VP8X", string(flags)+strings.Repeat("\x00", 9)) }
	tests := []struct {
		name, content string
		want          bool
	}{
		{"animated", webpFile(vp8x(0x12), riffChunk("ANIM", "\x00\x00\x00\x00\x00\x00"), riffChunk("ANMF", "frame")), true},
		{"anim chunk without flag", webpFile(vp8x(0), riffChunk("ANIM", "\x00\x00\x00\x00\x00\x00")), true},
		{"still with exif", webpFile(vp8x(0x08), riffChunk("VP8L", "abc"), riffChunk("EXIF", "x")), false},
		{"simple", webpFile(riffChunk("VP8 ", "abcd")), false},
		{"truncated vp8x", webpFile("VP8X\x0a\x00\x00\x00"), false},
		{"not webp", "RIFF\x04\x00\x00\x00WAVE", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAnimatedWebP(write(t, "a.webp", tt.content)); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTIFFTags(t *testing.T) {
	if got := TIFFTags(write(t, "a.tif", tiff(binary.BigEndian, 2))); len(got) != 2 || got[0] != 256 || got[1] != 262 {
		t.Errorf("got %v", got)
	}
	for _, c := range []string{"II+\x00\x08\x00\x00\x00", tiff(binary.LittleEndian, 2)[:6], "not a tiff"} {
		if got := TIFFTags(write(t, "a.tif", c)); got != nil {
			t.Errorf("TIFFTags(%q) = %v, want nil", c, got)
		}
	}
}

func TestPDFSignedAndPDFA(t *testing.T) {
	sig := "1 0 obj << /Type /Sig /Filter /Adobe.PPKLite /ByteRange [0 100 200 300] /Contents <00> >> endobj"
	pad := strings.Repeat("x", 1<<20-5)
	tests := []struct {
		name, content string
		signed, pdfa  bool
	}{
		{"plain", "%PDF-1.7\n1 0 obj << /Type /Catalog >> endobj", false, false},
		{"signed", "%PDF-1.7\n" + sig, true, false},
		{"empty signature field", "%PDF-1.7\n<< /FT /Sig /T (Signature1) >>", false, false},
		{"certified", "%PDF-1.7\n<< /Type /Catalog /Perms << /DocMDP 5 0 R >> >>", true, false},
		{"across read boundary", "%PDF-1.7\n" + pad + "/ByteRange [0 1 2 3] /Type /Sig", true, false},
		// The /Sig field dictionary sits in a compressed object stream.
		{"document timestamp", "%PDF-1.7\n<< /Type /DocTimeStamp /SubFilter /ETSI.RFC3161 /ByteRange [0 1 2 3] >>", true, false},
		{"pdfa", "%PDF-1.4\n<x:xmpmeta><rdf:Description pdfaid:part=\"2\" pdfaid:conformance=\"B\"/></x:xmpmeta>", false, true},
		{"pdfa element late", "%PDF-1.4\n" + pad + "<pdfaid:part>1</pdfaid:part>", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := write(t, "a.pdf", tt.content)
			if got := IsPDFSigned(p); got != tt.signed {
				t.Errorf("IsPDFSigned = %v, want %v", got, tt.signed)
			}
			if got := IsPDFA(p); got != tt.pdfa {
				t.Errorf("IsPDFA = %v, want %v", got, tt.pdfa)
			}
		})
	}
}

func TestHasPEOverlay(t *testing.T) {
	exe := plugin(t, "jhead.exe")
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if HasPEOverlay(exe) {
		t.Errorf("%s reported with overlay", exe)
	}
	if HasPEOverlay(write(t, "pad.exe", string(b)+strings.Repeat("\x00", 4096))) {
		t.Error("zero padding reported as overlay")
	}
	if !HasPEOverlay(write(t, "pyi.exe", string(b)+"payload MEI\x0c\x0b\x0a\x0b\x0e")) {
		t.Error("appended payload not detected")
	}
	if HasPEOverlay(write(t, "hdr.exe", peFile()+"data")) {
		t.Error("image without sections reported with overlay")
	}
	for _, p := range []string{`C:\Windows\explorer.exe`, `C:\Windows\System32\ntoskrnl.exe`} {
		if _, err := os.Stat(p); err == nil && HasPEOverlay(p) {
			t.Errorf("%s: certificate counted as overlay", p)
		}
	}
}

func box(typ string, payload ...string) string {
	p := strings.Join(payload, "")
	return string(binary.BigEndian.AppendUint32(nil, uint32(8+len(p)))) + typ + p
}

func TestIsFragmentedMP4(t *testing.T) {
	ftyp := box("ftyp", "isom\x00\x00\x02\x00")
	large := "\x00\x00\x00\x01mdat" + string(binary.BigEndian.AppendUint64(nil, 20)) + "data"
	tests := []struct {
		name, content string
		want          bool
	}{
		{"plain", ftyp + box("moov", box("mvhd", "x"), box("trak", "x")) + box("mdat", "data"), false},
		{"moof", ftyp + box("moov", box("mvhd", "x")) + box("moof", box("mfhd", "x")) + box("mdat", "d"), true},
		{"empty moov", ftyp + box("moov", box("mvhd", "x"), box("mvex", box("trex", "x"))), true},
		{"largesize", ftyp + large + box("moof", ""), true},
		{"size zero last box", ftyp + box("moov", box("mvhd", "x")) + "\x00\x00\x00\x00mdat....", false},
		{"bad size", ftyp + "\x00\x00\x00\x04moof", false},
		{"truncated", ftyp + box("moov", box("mvhd", "x"))[:10], false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsFragmentedMP4(write(t, "a.mp4", tt.content)); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// oggPage builds an Ogg page; bos marks it as the first page of a logical stream.
func oggPage(bos bool, data string) string {
	h := []byte("OggS\x00\x00")
	if bos {
		h[5] = 2
	}
	h = append(h, make([]byte, 20)...)
	var segs []byte
	for n := len(data); ; n -= 255 {
		segs = append(segs, byte(min(n, 255)))
		if n < 255 {
			break
		}
	}
	return string(append(append(h, byte(len(segs))), segs...)) + data
}

func TestOggCodecs(t *testing.T) {
	vorbis, theora := oggPage(true, "\x01vorbis....."), oggPage(true, "\x80theora.....")
	data := oggPage(false, strings.Repeat("d", 600))
	tests := []struct {
		name, content string
		want          []string
	}{
		{"vorbis", vorbis + data + data, []string{"vorbis"}},
		{"theora vorbis", theora + vorbis + data, []string{"theora", "vorbis"}},
		{"chained", vorbis + data + oggPage(true, "OpusHead....") + data, []string{"vorbis", "opus"}},
		{"flac speex skeleton", oggPage(true, "\x7fFLAC...") + oggPage(true, "Speex   ...") + oggPage(true, "fishead\x00..."), []string{"flac", "speex", "skeleton"}},
		{"unknown codec", oggPage(true, "\x80kate...."), []string{""}},
		{"junk after pages", vorbis + data + "TAG", []string{"vorbis", ""}},
		{"not ogg", "RIFF", []string{""}},
		{"empty", "", []string{""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OggCodecs(write(t, "a.ogg", tt.content)); strings.Join(got, ",") != strings.Join(tt.want, ",") || len(got) != len(tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// ebml encodes an element with a 1 byte size (payloads < 127 bytes) or an 8 byte size.
func ebml(id uint32, payload ...string) string {
	p := strings.Join(payload, "")
	idb := binary.BigEndian.AppendUint32(nil, id)
	for len(idb) > 1 && idb[0] == 0 {
		idb = idb[1:]
	}
	if len(p) < 127 {
		return string(idb) + string([]byte{0x80 | byte(len(p))}) + p
	}
	return string(idb) + "\x01" + string(binary.BigEndian.AppendUint64(nil, uint64(len(p)))[1:]) + p
}

func TestMatroskaV4(t *testing.T) {
	head := ebml(0x1a45dfa3, ebml(0x4282, "matroska"))
	seg := func(children ...string) string { return head + ebml(0x18538067, children...) }
	entry := func(children ...string) string { return ebml(0x1654ae6b, ebml(0xae, children...)) }
	codec := ebml(0x86, "V_MPEG4/ISO/AVC") + ebml(0x63a2, strings.Repeat("\x55\xb0", 100))
	cluster := ebml(0x1f43b675, ebml(0xe7, "\x00"))
	tests := []struct {
		name, content string
		want          bool
	}{
		{"plain", seg(ebml(0x114d9b74, "seekhead"), entry(codec, ebml(0xe0, ebml(0xb0, "\x01\x40"))), cluster), false},
		{"colour", seg(entry(codec, ebml(0xe0, ebml(0x55b0, ebml(0x55b9, "\x01")))), cluster), true},
		{"codec delay", seg(entry(ebml(0x86, "A_OPUS"), ebml(0x56aa, "\x01"))), true},
		{"unknown size segment", head + "\x18\x53\x80\x67\x01\xff\xff\xff\xff\xff\xff\xff" + entry(codec), false},
		{"no tracks before cluster", seg(cluster, entry(codec)), true},
		{"broken tracks", seg("\x16\x54\xae\x6b\x85\xae\x90"), true},
		{"not matroska", "RIFF....", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatroskaV4(write(t, "a.mkv", tt.content)); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasZIPUnicodeExtra(t *testing.T) {
	mk := func(extra []byte) string {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		w, _ := zw.CreateHeader(&zip.FileHeader{Name: "caf?.txt", Extra: extra})
		w.Write([]byte("x"))
		zw.Close()
		return b.String()
	}
	ut := []byte{0x55, 0x54, 5, 0, 1, 0, 0, 0, 0}
	upath := append(append([]byte{}, ut...), 0x75, 0x70, 6, 0, 1, 0, 0, 0, 0, 'c')
	if HasZIPUnicodeExtra(write(t, "a.zip", mk(ut))) {
		t.Error("timestamp extra reported as unicode")
	}
	if !HasZIPUnicodeExtra(write(t, "b.zip", mk(upath))) {
		t.Error("unicode path extra not detected")
	}
	if HasZIPUnicodeExtra(write(t, "c.zip", "PK\x03\x04junk")) {
		t.Error("damaged zip reported as unicode")
	}
}

func TestHasZIPDirs(t *testing.T) {
	mk := func(names ...string) string {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		for _, n := range names {
			zw.Create(n)
		}
		zw.Close()
		return b.String()
	}
	for _, tt := range []struct {
		content string
		want    bool
	}{
		{mk("a.txt", "d/b.txt"), false},
		{mk("a.txt", "empty/"), true},
		{"PK\x03\x04junk", true},
	} {
		if got := HasZIPDirs(write(t, "a.zip", tt.content)); got != tt.want {
			t.Errorf("HasZIPDirs(%q) = %v, want %v", tt.content, got, tt.want)
		}
	}
}
