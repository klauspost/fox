// Package pipeline defines the tool chain used for each file format.
package pipeline

import (
	"bufio"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"debug/pe"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/klauspost/fox/internal/config"
	"github.com/klauspost/fox/internal/detect"
)

// Step is one tool invocation in a pipeline.
//
// Args may contain the placeholders {in} and {out}:
//   - With {out}: the tool reads {in} (the current best version, must not be modified) and writes {out}.
//   - Without {out}: {in} is a private copy that the tool optimizes in place.
//   - Stdio: the current file is fed on stdin and stdout is captured as the output.
//   - Func: custom Go logic; it must write its result to out. Args, if set, are only informational.
//
// The result replaces the current file only if it is at least 8 bytes, smaller, and Accept (if set) agrees.
type Step struct {
	Tool   string
	Args   []string
	Stdio  bool
	Func   func(ctx context.Context, x *Exec, in, out string) error
	Accept func(cur, cand int64) bool
}

// Exec gives Func steps access to tools.
type Exec struct {
	// Run executes a tool with the given args. Placeholders are not expanded.
	Run func(ctx context.Context, tool string, args ...string) error
	// RunStdio executes a tool with stdin/stdout connected to files. Empty stdin means no input.
	RunStdio func(ctx context.Context, tool, stdin, stdout string, args ...string) error
	// Dir is a scratch directory owned by the step; Run executes tools with Dir as working directory.
	Dir string
}

// For returns the steps for a file of format f. path is the original file, used only for content checks.
// A non-empty skip reason (e.g. "disabled in settings", "signed executable") means the file must not be processed.
func For(f detect.Format, path string, s *config.Settings) (steps []Step, skip string) {
	build, ok := builders[f]
	if !ok {
		return nil, "unsupported format"
	}
	b := &builder{s: s, path: path, ext: strings.ToLower(filepath.Ext(path)), l: s.Level}
	if skip := build(b); skip != "" {
		return nil, skip
	}
	if len(b.steps) == 0 {
		return nil, "nothing to do with current settings"
	}
	return b.steps, ""
}

const disabled = "disabled in settings"

var builders = map[detect.Format]func(*builder) string{
	detect.JPEG: (*builder).jpeg, detect.PNG: (*builder).png, detect.ICO: (*builder).ico,
	detect.GIF: (*builder).gif, detect.WebP: (*builder).webp, detect.BMP: (*builder).bmp,
	detect.TIFF: (*builder).tiff, detect.SVG: (*builder).svg, detect.PDF: (*builder).pdf,
	detect.HTML: (*builder).html, detect.CSS: (*builder).css, detect.JS: (*builder).js,
	detect.ZIP: (*builder).zip, detect.GZ: (*builder).gz,
	detect.TAR: (*builder).tar, detect.SevenZ: (*builder).sevenZ, detect.SQLite: (*builder).sqlite,
	detect.FLAC: (*builder).flac, detect.WAV: (*builder).wav, detect.MP3: (*builder).mp3,
	detect.OGG: (*builder).ogg, detect.MP4: (*builder).mp4, detect.MKV: (*builder).mkv,
	detect.EXE: (*builder).exe, detect.DLL: (*builder).dll,
}

type builder struct {
	s     *config.Settings
	path  string
	ext   string
	l     int
	steps []Step
}

// add appends a step. Each part is split on spaces, so empty parts (from when) vanish.
func (b *builder) add(tool string, parts ...string) {
	var args []string
	for _, p := range parts {
		args = append(args, strings.Fields(p)...)
	}
	b.steps = append(b.steps, Step{Tool: tool, Args: args})
}

func when(cond bool, s string) string {
	if cond {
		return s
	}
	return ""
}

func pick(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

var itoa = strconv.Itoa

// Level helpers; l is the 1..9 level.

func iter(l int) int         { return l*l*l/25 + 1 } // 1, 1, 2, 3, 6, 9, 14, 21, 30
func pingoLevel(l int) int   { return l * 4 / 9 }
func oxipngLevel(l int) int  { return l * 6 / 9 }
func truepngLevel(l int) int { return l*3/9 + 1 }

// The original's max(l*3/9-3, 0) is always 0, the slowest strategy.
func pngoutLevel(l int) int { return 3 - l*3/9 } // 3, 3, 2, 2, 2, 1, 1, 1, 0

func ectLevel(l int) int {
	if n := l*8/9 + 1; n < 9 {
		return n
	}
	// ECT encodes "-9" as 9 iterations of everything; 90032 is faster and usually smaller.
	return 90032
}

func upxFlags(l int) string {
	switch {
	case l < 3:
		return "-1"
	case l < 5:
		return "-9"
	case l < 7:
		return "-9 --best"
	case l < 9:
		return "-9 --best --lzma"
	}
	return "-9 --best --lzma --ultra-brute"
}

func flacFlags(l int) string {
	switch {
	case l < 3:
		return "-1"
	case l < 5:
		return "-8 --best"
	case l < 7:
		return "-8 --best -e"
	}
	return "-8 --best -ep"
}

func (b *builder) leanifyIter() string {
	n := iter(b.l)
	if b.s.LeanifyIterations > 0 {
		n = b.s.LeanifyIterations
	}
	return "-i " + itoa(n)
}

func (b *builder) ect() string { return "-" + itoa(ectLevel(b.l)) }

func (b *builder) adv(tool string) { b.add(tool, "-z -q -4 -i", itoa(iter(b.l)), "{in}") }

func (b *builder) stdio(tool string) { b.steps = append(b.steps, Step{Tool: tool, Stdio: true}) }

// displayMeta reports whether an image has metadata that affects how it displays, so it must keep all metadata:
// without its EXIF Orientation a rotated photo displays sideways, without its color profile a wide-gamut image
// displays with shifted colors.
func displayMeta(path string) bool { return detect.Orientation(path) > 1 || detect.ColorManaged(path) }

func (b *builder) jpeg() string {
	// The tools drop everything after the primary image's end, and with it the gain map that HDR displays use.
	if detect.HasGainMap(b.path) {
		return "HDR gain map JPEG"
	}
	j := b.s.JPEG
	lossy, meta := j.Lossy, j.KeepMetadata || displayMeta(b.path)
	// pingo drops COM comments even with -nostrip.
	if !meta {
		b.add("pingo", "-s"+itoa(pingoLevel(b.l)), pick(lossy, "-quality=95", "-lossless"), "{in}")
	}
	if lossy && !meta {
		b.add("cjpegli", "-q 70 {in} {out}")
		b.add("guetzli", "--auto --quality 90 {in} {out}")
	}
	if lossy {
		b.add("jpeg-recompress", "--method smallfry --quality high --min 60 --subsample disable --quiet",
			when(!meta, "--strip"), when(b.l >= 5, "--accurate"), "{in} {out}")
	}
	// jhead corrupts CMYK JPEGs. With metadata kept it would only run -zt, which also cuts zero bytes
	// that belong to the EXIF structure. No -autorot: it needs jpegtran on PATH and -trim crops pixels.
	if !meta && !detect.IsJPEGCMYK(b.path) {
		b.add("jhead", "-q -purejpg -di -dx -dt -zt {in}")
	}
	b.add("leanify", "-q -p", when(meta, "--keep-exif --keep-icc --jpeg-keep-all"),
		when(j.Arithmetic, "--jpeg-arithmetic"), b.leanifyIter(), "{in}")
	if lossy {
		b.add("magick", "-quiet {in} -interlace Plane -define jpeg:optimize-coding=true", when(!meta, "-strip"), "{out}")
	}
	arith, cp := when(j.Arithmetic, "-arithmetic"), pick(meta, "-copy all", "-copy none")
	// jpegoptim's own parallelism is pointless: fox already runs one file per core.
	b.add("jpegoptim", "-o -q --all-progressive --nofix", when(!meta, "--strip-all"), when(j.Arithmetic, "--all-arith"), "-w 1 {in}")
	b.add("jpegtran", "-outfile {out} -progressive -optimize", arith, cp, "{in}")
	b.add("mozjpegtran", "-outfile {out} -progressive -optimize -perfect", arith, cp, "{in}")
	b.add("ect", "-quiet --mt-deflate --allfilters -progressive", when(!meta, "-strip"), when(!lossy, "--strict"), b.ect(), "{in}")
	return ""
}

func (b *builder) png() string {
	p := b.s.PNG
	lossy, meta := p.Lossy, p.KeepMetadata || displayMeta(b.path)
	// Android 9-patch images get broken by advpng, deflopt, pngoptimizer, pngout, pngrewrite and truepng.
	apng, nine := detect.IsAPNG(b.path), detect.IsNinePatch(b.path)
	// apngopt drops all ancillary chunks.
	if apng && !meta {
		b.add("apngopt", "{in} {out}")
	}
	// pngquant drops eXIf even without --strip, and PngOptimizer drops eXIf, iTXt, zTXt and most other
	// chunks, even with -KeepTextualData.
	if !nine && !meta {
		if lossy && !apng {
			b.add("pngquant", "--strip --quality=85-95 --speed 1 --force --output {out} {in}")
		}
		b.add("pngoptimizer", "-file:{in}")
	}
	// truepng -a1 and oxipng --alpha only change the color of fully transparent pixels.
	// truepng -g1 would apply gAMA to the pixels; -g0 just drops it like the other metadata.
	if !apng && !nine {
		b.add("truepng", "-o"+itoa(truepngLevel(b.l)), pick(meta, "-md keep all", "-tz -md remove all -a1 -g0"),
			when(lossy, "-l"), "/i0 /nc /tz /quiet /y /out {out} {in}")
		b.add("pngout", "/q /y /r /d0 /mincodes0", pick(meta, "/k1", "/kacTL,fcTL,fdAT"),
			"/s"+itoa(pngoutLevel(b.l)), "{in} {out}")
	}
	// "--strip all" turns APNGs into plain PNGs; "--strip safe" still drops text and EXIF chunks.
	b.add("oxipng", "--zopfli --alpha --quiet -o"+itoa(oxipngLevel(b.l)), when(lossy, "--scale16"),
		when(!meta, pick(apng, "--strip safe", "--strip all")), "{in}")
	if !apng {
		if !meta {
			b.add("leanify", "-q -p", b.leanifyIter(), "{in}")
		}
		n := iter(b.l)
		if b.s.PNGWolfIterations > 0 {
			n = b.s.PNGWolfIterations
		}
		b.add("pngwolf", "--out-deflate=zopfli,iter="+itoa(n), "--in={in} --out={out}")
		// pngrewrite and advpng drop text, EXIF and color chunks.
		if !nine && !meta {
			b.add("pngrewrite", "{in} {out}")
			b.adv("advpng")
		}
	}
	// ECT keeps APNG chunks only with --reuse and without -strip.
	b.add("ect", "-quiet --mt-deflate --allfilters", pick(apng, "--reuse", when(!meta, "-strip")), b.ect(), "{in}")
	// The 9-patch border must stay pure black, so no lossy pingo.
	b.add("pingo", "-s"+itoa(pingoLevel(b.l)), when(meta, "-nostrip"), pick(lossy && !nine, "-quality=95", "-lossless"), "{in}")
	b.deflate(!apng && !nine, when(meta, "/k"))
	return ""
}

// deflate adds the deflopt, defluff, deflopt tail shared by deflate based formats.
func (b *builder) deflate(deflopt bool, flags string) {
	if deflopt {
		b.add("deflopt", "/a /b /s", flags, "{in}")
	}
	b.stdio("defluff")
	if deflopt {
		b.add("deflopt", "/a /b /s", flags, "{in}")
	}
}

func (b *builder) ico() string {
	// Leanify drops the gAMA, cHRM and cICP chunks of embedded PNGs even with --keep-icc.
	if detect.ColorManaged(b.path) {
		return "ICO with color-managed PNG"
	}
	// Leanify drops IPTC metadata.
	if !b.s.PNG.KeepMetadata {
		b.add("leanify", "-q -p", b.leanifyIter(), "{in}")
	}
	return ""
}

func (b *builder) gif() string {
	g := b.s.GIF
	meta := g.KeepMetadata || detect.ColorManaged(b.path)
	// ImageMagick requantizes frames that have their own palette.
	if g.Lossy {
		b.add("magick", "-quiet {in} -layers optimize -compress LZW", when(!meta, "-strip"), "{out}")
	}
	b.add("gifsicle", "-w -j --no-conserve-memory -o {out} -O3",
		when(!meta, "--no-comments --no-extensions --no-names"), when(g.Lossy, "--lossy=85"), "{in}")
	return ""
}

func (b *builder) webp() string {
	// cwebp can't read animations and pingo gains nothing on them.
	if detect.IsAnimatedWebP(b.path) {
		return "animated WebP"
	}
	lossy, meta := b.s.WebP.Lossy, b.s.WebP.KeepMetadata || displayMeta(b.path)
	// Lossy pingo drops all metadata of a lossless input, even with -nostrip.
	if !lossy || !meta {
		b.add("pingo", "-s"+itoa(pingoLevel(b.l)), pick(lossy, "-quality=95", "-lossless"), when(meta, "-nostrip"), "{in}")
	}
	// Lossless cwebp reproduces the decoded pixels of a lossy input exactly.
	b.add("cwebp", "-mt -quiet", pick(lossy, "-m "+itoa(b.l*6/9), "-z "+itoa(b.l)+" -lossless"),
		when(meta, "-metadata all"), "{in} -o {out}")
	return ""
}

func (b *builder) bmp() string {
	meta := b.s.BMP.KeepMetadata || detect.ColorManaged(b.path)
	b.add("magick", "-quiet {in} -compress RLE", when(!meta, "-strip"), "{out}")
	// ImageWorsener drops the color profile.
	if !meta {
		b.add("imagew", "-opt bmp:version=auto -noresize -zipcmprlevel 9 -outfmt bmp -compress rle {in} {out}")
	}
	return ""
}

// geoTIFFTags hold GeoTIFF georeferencing.
var geoTIFFTags = []uint16{33550, 33922, 34264, 34735}

func (b *builder) tiff() string {
	// ImageMagick cannot keep TIFF metadata.
	if b.s.TIFF.KeepMetadata {
		return ""
	}
	// It also drops tags it doesn't know, even without -strip.
	switch tags := detect.TIFFTags(b.path); {
	case tags == nil:
		return "unsupported TIFF"
	case slices.Contains(tags, 37724):
		return "TIFF with Photoshop layers"
	case slices.ContainsFunc(tags, func(t uint16) bool { return slices.Contains(geoTIFFTags, t) }):
		return "GeoTIFF"
	}
	// Without -strip ImageMagick keeps the color profile.
	b.add("magick", "-quiet {in} -compress ZIP", when(!detect.ColorManaged(b.path), "-strip"), "{out}")
	return ""
}

func (b *builder) svg() string {
	if !b.s.Web.SVG {
		return disabled
	}
	// Both tools drop the whitespace between <tspan>s ("Hello <tspan>big</tspan>" renders "Hellobig").
	if data, _ := os.ReadFile(b.path); bytes.Contains(data, []byte("<text")) || bytes.Contains(data, []byte(":text")) {
		return "SVG with text"
	}
	b.add("leanify", "-q -p", b.leanifyIter(), "{in}")
	b.add("minify", "--type svg -o {out} {in}")
	return ""
}

func (b *builder) pdf() string {
	pd := b.s.PDF
	lossless := pd.Profile == "none"
	switch {
	case detect.IsPDFSigned(b.path):
		return "signed PDF"
	// qpdf's object streams break PDF/A-1, and none of the tools preserve conformance deliberately.
	case detect.IsPDFA(b.path):
		return "PDF/A document"
	case !lossless && pd.SkipLayered && detect.IsPDFLayered(b.path):
		return "layered PDF"
	}
	b.add("mutool", "clean -g -z {in} {out}")
	// Ghostscript always re-encodes images, so it only runs when downsampling was asked for.
	// It breaks Illustrator files.
	if !lossless && b.ext != ".ai" {
		var profile string
		if pd.Profile == "custom" {
			dpi := itoa(pd.DPI)
			profile = "-dPDFSETTINGS=/ebook -dDownsampleColorImages=true -dColorImageResolution=" + dpi +
				" -dDownsampleGrayImages=true -dGrayImageResolution=" + dpi +
				" -dDownsampleMonoImages=true -dMonoImageResolution=" + dpi
		} else {
			profile = "-dPDFSETTINGS=/" + pd.Profile
		}
		b.add("gs", profile, "-dColorImageDownsampleType=/Bicubic -dGrayImageDownsampleType=/Bicubic",
			"-dMonoImageDownsampleType=/Bicubic -dOptimize=true -dConvertCMYKImagesToRGB=true",
			"-dColorConversionStrategy=/sRGB -dPrinted=false -q -dBATCH -dNOPAUSE -dSAFER -dNOPROMPT",
			"-sDEVICE=pdfwrite -dDetectDuplicateImages=true -dAutoRotatePages=/None -dCompatibilityLevel=1.4",
			"-sOutputFile={out} {in}")
		// A huge reduction usually means an encrypted PDF that Ghostscript could not read.
		b.steps[len(b.steps)-1].Accept = func(cur, cand int64) bool { return cand > 3000 && cur > 20000 }
	}
	b.add("cpdf", "-squeeze {in} -o {out}")
	// --optimize-images recompresses images as JPEG, which is lossy.
	b.add("qpdf", "{in} --compress-streams=y --decode-level=generalized --recompress-flate --compression-level=9",
		when(!lossless, "--optimize-images"), "--object-streams=generate {out}")
	return ""
}

func (b *builder) html() string {
	if !b.s.Web.HTML {
		return disabled
	}
	// minify keeps <pre> and <textarea> intact, but can't know which other elements CSS makes whitespace
	// sensitive. It also collapses legacy <xmp>/<listing>/<plaintext> and the spaces around inline SVG <tspan>s.
	if data, _ := os.ReadFile(b.path); preWhitespace.Match(data) {
		return "HTML with whitespace-sensitive content"
	}
	b.add("minify", "--type html -o {out} {in}")
	return ""
}

var preWhitespace = regexp.MustCompile(`(?i)white-space(-collapse)?\s*:\s*(pre|break-spaces)|<(xmp|listing|plaintext|text)\b`)

func (b *builder) css() string {
	if !b.s.Web.CSS {
		return disabled
	}
	// No csstidy: it is CSS 2.1 only and deletes @layer/@container blocks, nesting and calc() spacing.
	b.add("minify", "--type css -o {out} {in}")
	return ""
}

func (b *builder) js() string {
	extra := slices.ContainsFunc(b.s.Web.JSExtensions, func(e string) bool {
		return strings.EqualFold("."+strings.TrimPrefix(e, "."), b.ext)
	})
	if !b.s.Web.JS && !extra {
		return disabled
	}
	// No jsmin: it truncates nested template literals.
	// Rewritten numbers ("1.0" to "1") change the type some JSON parsers decode.
	b.add("minify", pick(b.ext == ".json", "--type json --json-keep-numbers", "--type js"), "-o {out} {in}")
	return ""
}

func (b *builder) zip() string {
	a, j := b.s.Archive, b.s.JPEG
	// Every tool here drops extra fields, which would revert those names to their legacy encoding.
	if detect.HasZIPUnicodeExtra(b.path) {
		return "ZIP with Unicode name fields"
	}
	// Leanify, ECT and advzip drop archive comments, file comments and extra fields.
	if !a.KeepMetadata {
		// No --jpeg-arithmetic: few consumers of zip based documents decode arithmetic JPEGs.
		// Inner JPEGs keep EXIF, since the orientation of each can't be checked.
		b.add("leanify", "-q -p", b.leanifyIter(), "--zip-deflate", pick(a.Recurse, "--keep-exif", "-d 1"),
			when(a.Recurse && j.KeepMetadata, "--keep-icc --jpeg-keep-all"), "{in}")
		b.add("ect", "-quiet --mt-deflate -zip -strip --disable-jpg", when(!a.Recurse, "--disable-png"), b.ect(), "{in}")
		b.adv("advzip")
	}
	// DeflOpt drops directory entries, so empty directories would vanish.
	b.deflate(!detect.HasZIPDirs(b.path), when(a.KeepMetadata, "/c /k"))
	return ""
}

func (b *builder) gz() string {
	if skip := gzipCheck(b.path); skip != "" {
		return skip
	}
	meta := b.s.Archive.KeepMetadata
	if !meta {
		b.steps = append(b.steps, Step{Tool: "gzip", Func: libdeflate})
		// Leanify would also rewrite the files inside a .tar.gz or .svgz.
		b.add("leanify", "-q -p", b.leanifyIter(), pick(b.s.Archive.Recurse, "--keep-exif", "-d 1"), "{in}")
	}
	b.adv("advdef")
	// zRecompress drops the stored name and time; ECT drops the comment and resets the time even without -strip.
	if !meta {
		b.add("zrecompress", "-tgz {in}")
		b.add("ect", "-quiet --mt-deflate --allfilters -gzip -strip", b.ect(), "{in}")
	}
	// DeflOpt /c without /k drops a gzip comment but leaves its header flag set, corrupting the file.
	b.deflate(true, when(meta, "/c /k"))
	return ""
}

// libdeflate recompresses with libdeflate's gzip, which has a stronger level 12 than GNU gzip.
// Going through stdin drops the stored name and timestamp.
func libdeflate(ctx context.Context, x *Exec, in, out string) error {
	raw := filepath.Join(x.Dir, "raw")
	if err := x.RunStdio(ctx, "gzip", in, raw, "-c", "-d"); err != nil {
		return err
	}
	return x.RunStdio(ctx, "gzip", raw, out, "-c", "-12")
}

// gzipCheck rejects files with data after the first member (concatenated or BGZF): deflopt, defluff and
// zRecompress silently keep only the first member, and recompressing BGZF invalidates its index.
func gzipCheck(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "damaged gzip"
	}
	defer f.Close()
	br := bufio.NewReader(f)
	zr, err := gzip.NewReader(br)
	if err != nil {
		return "damaged gzip"
	}
	zr.Multistream(false)
	if _, err := io.Copy(io.Discard, zr); err != nil {
		return "damaged gzip"
	}
	if _, err := br.ReadByte(); err != io.EOF {
		return "multi-member gzip"
	}
	return ""
}

func (b *builder) tar() string {
	// A tar has nothing to optimize besides its contents.
	// Inner JPEGs keep EXIF, since the orientation of each can't be checked.
	if b.s.Archive.Recurse {
		b.add("leanify", "-q -p --keep-exif", b.leanifyIter(), "{in}")
	}
	return ""
}

func (b *builder) sevenZ() string {
	// Very slow, and needs ~11.5 x dictionary size of RAM.
	if b.l > 7 {
		b.steps = append(b.steps, Step{Tool: "m7zrepacker", Func: m7zRepack})
	}
	return ""
}

// m7zRepack runs m7zRepacker, which leaves its input alone and writes "<name> [Repacked_<args>].7z" next to it.
// It resolves relative paths against its own directory.
func m7zRepack(ctx context.Context, x *Exec, in, out string) error {
	in, err := filepath.Abs(in)
	if err != nil {
		return err
	}
	if err := x.Run(ctx, "m7zrepacker", "-m1", "-d1024", "-mem2048", in); err != nil {
		return err
	}
	return os.Rename(strings.TrimSuffix(in, filepath.Ext(in))+" [Repacked_m1_d1024_mem2048].7z", out)
}

func (b *builder) sqlite() string {
	// A -wal/-journal would be applied to the rewritten file on next open and corrupt it; a -shm (or an
	// empty -wal) means a connection may be open and write while fox works on its copy.
	for _, s := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(b.path + s); !errors.Is(err, fs.ErrNotExist) {
			return "database in use"
		}
	}
	b.steps = append(b.steps, Step{Tool: "sqlite3", Func: sqliteVacuum})
	return ""
}

// sqliteVacuum rebuilds the database. Unlike a .dump/restore, VACUUM INTO keeps user_version,
// application_id and page size, which applications rely on.
func sqliteVacuum(ctx context.Context, x *Exec, in, out string) error {
	_ = os.Remove(out)
	if err := x.Run(ctx, "sqlite3", "-readonly", in, "VACUUM INTO '"+strings.ReplaceAll(out, "'", "''")+"'"); err != nil {
		return err
	}
	// VACUUM INTO always writes rollback journal mode; WAL is a persistent property of the file.
	if walMode(in) {
		return x.Run(ctx, "sqlite3", out, "PRAGMA journal_mode=WAL")
	}
	return nil
}

// walMode checks the file format read/write version bytes of the database header.
func walMode(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var h [20]byte
	_, err = f.ReadAt(h[:], 0)
	return err == nil && h[18] == 2 && h[19] == 2
}

func (b *builder) flac() string {
	a := b.s.Audio
	// A FLAC decodes to a canonical WAV, so shntool strip has nothing to remove; trim needs a WAV round trip.
	if !a.KeepMetadata && a.TrimSilence {
		b.steps = append(b.steps, Step{Tool: "shntool", Func: flacTrim(flacFlags(b.l))})
	}
	// FLAC to FLAC keeps all metadata; --keep-foreign-metadata is rejected for FLAC input.
	b.add("flac", "--force -s", flacFlags(b.l), "{in}")
	// flacout drops tags and pictures.
	if b.l >= 9 && !a.KeepMetadata {
		b.add("flacout", "/q /y {in} {out}")
	}
	return ""
}

func flacTrim(flags string) func(ctx context.Context, x *Exec, in, out string) error {
	return func(ctx context.Context, x *Exec, in, out string) error {
		wav := filepath.Join(x.Dir, "pcm.wav")
		if err := x.Run(ctx, "flac", "-d", "-s", "-f", "-o", wav, in); err != nil {
			return err
		}
		if err := shntool(ctx, x, "trim", wav, wav); err != nil {
			return err
		}
		return x.Run(ctx, "flac", append(strings.Fields("-s -f "+flags), "-o", out, wav)...)
	}
}

func (b *builder) wav() string {
	a := b.s.Audio
	if !a.KeepMetadata {
		b.steps = append(b.steps, Step{Tool: "shntool", Func: shntoolFunc("strip")})
		if a.TrimSilence {
			b.steps = append(b.steps, Step{Tool: "shntool", Func: shntoolFunc("trim")})
		}
	}
	return ""
}

func shntoolFunc(mode string) func(ctx context.Context, x *Exec, in, out string) error {
	return func(ctx context.Context, x *Exec, in, out string) error { return shntool(ctx, x, mode, in, out) }
}

// shntool cannot name its output; it writes <name>-stripped.wav / <name>-trimmed.wav into -d.
func shntool(ctx context.Context, x *Exec, mode, in, out string) error {
	if err := x.Run(ctx, "shntool", mode, "-q", "-O", "always", "-d", x.Dir, in); err != nil {
		return err
	}
	name := strings.TrimSuffix(filepath.Base(in), filepath.Ext(in))
	suffix := map[string]string{"strip": "-stripped.wav", "trim": "-trimmed.wav"}[mode]
	return os.Rename(filepath.Join(x.Dir, name+suffix), out)
}

func (b *builder) mp3() string {
	args := []string{"-z", "-a", "", "-A", "-f"}
	if !b.s.Audio.KeepMetadata {
		args = append([]string{"-t", "-s"}, args...)
	}
	// mp3packer zeroes the encoder delay and padding of a LAME tag whose CRC doesn't match (ffmpeg writes such
	// tags), which adds silence, and it drops granules it can't fit into the bit reservoir.
	b.steps = append(b.steps, sameAudio("mp3packer", append(args, "{in}", "{out}")...))
	return ""
}

// sameAudio returns a step that runs tool and keeps its output only if ffmpeg decodes it to the same audio.
// Args are expanded by the step itself and kept in the Step for display.
func sameAudio(tool string, args ...string) Step {
	return Step{Tool: tool, Args: args, Func: func(ctx context.Context, x *Exec, in, out string) error {
		r := strings.NewReplacer("{in}", in, "{out}", out)
		a := make([]string, len(args))
		for i, s := range args {
			a[i] = r.Replace(s)
		}
		if err := x.Run(ctx, tool, a...); err != nil {
			return err
		}
		var sums [2][]byte
		for i, f := range []string{in, out} {
			sum := filepath.Join(x.Dir, itoa(i)+".md5")
			if err := x.Run(ctx, "ffmpeg", "-y", "-v", "error", "-i", f, "-map", "0:a", "-f", "md5", sum); err != nil {
				return err
			}
			var err error
			if sums[i], err = os.ReadFile(sum); err != nil {
				return err
			}
		}
		if !bytes.Equal(sums[0], sums[1]) {
			return errors.New(tool + ": decoded audio changed")
		}
		return nil
	}}
}

// The extension doesn't tell: .ogg files often hold Theora video, and optivorbis would keep only the audio.
func (b *builder) ogg() string {
	codecs := detect.OggCodecs(b.path)
	switch {
	case len(codecs) == 0:
		return "damaged Ogg"
	case !slices.ContainsFunc(codecs, func(c string) bool { return c != "vorbis" }):
		// optivorbis may put a short file's audio into one page, from which ffmpeg can't tell how many leading
		// samples to discard.
		b.steps = append(b.steps, sameAudio("optivorbis", strings.Fields(pick(b.s.Audio.KeepMetadata,
			"--vendor_string_action copy --comment_fields_action copy",
			"--vendor_string_action empty --comment_fields_action delete")+" {in} {out}")...))
	case !slices.ContainsFunc(codecs, func(c string) bool { return c != "vorbis" && c != "theora" }):
		b.ffmpeg()
		b.remux("rehuff_theora", "{in} {out}")
	default:
		codecs = slices.DeleteFunc(codecs, func(c string) bool { return c == "vorbis" || c == "theora" })
		for i, c := range codecs {
			codecs[i] = cmp.Or(c, "unknown")
		}
		slices.Sort(codecs)
		return "unsupported Ogg stream (" + strings.Join(slices.Compact(codecs), ", ") + ")"
	}
	return ""
}

// remux adds a step that must not lose data. A lossless remux can't halve a file,
// so a much smaller result means a tool dropped streams or fragments.
func (b *builder) remux(tool string, parts ...string) {
	b.add(tool, parts...)
	b.steps[len(b.steps)-1].Accept = func(cur, cand int64) bool { return cand >= cur/2 }
}

func (b *builder) ffmpeg() {
	meta := b.s.Video.KeepMetadata
	if slices.Contains([]string{".m4a", ".m4b", ".mka"}, b.ext) {
		meta = b.s.Audio.KeepMetadata
	}
	// Only global tags: stream tags hold the track language and title players select by.
	// -xerror: ffmpeg exits 0 after a demuxing error, e.g. at the second link of a chained Ogg,
	// leaving a truncated output.
	b.remux("ffmpeg", "-y -v error -xerror -i {in} -c copy -map 0", when(!meta, "-map_metadata:g -1"), "{out}")
}

func (b *builder) mp4() string {
	b.ffmpeg()
	// mp4file keeps only the moov of a fragmented file and drops the fragments.
	if !detect.IsFragmentedMP4(b.path) {
		b.remux("mp4file", "--optimize -q {in}")
	}
	return ""
}

func (b *builder) mkv() string {
	b.ffmpeg()
	// mkclean drops Matroska v4 elements (HDR Colour, Opus CodecDelay, ...) unless told to write
	// DocType version 4, which ffmpeg refuses to open. It isn't used for WebM at all.
	if b.ext != ".webm" && !detect.MatroskaV4(b.path) {
		b.remux("mkclean", "--optimize --unsafe --quiet {in} {out}")
	}
	return ""
}

func (b *builder) exe() string {
	e := b.s.Exe
	switch {
	case !e.Enabled:
		return disabled
	case detect.IsSigned(b.path):
		return "signed executable"
	case detect.IsSFX(b.path):
		return "installer/self-extracting"
	case detect.HasPEOverlay(b.path):
		return overlay
	}
	// Leanify strips relocations unconditionally, which disables ASLR and fails under mandatory ASLR.
	if !aslr(b.path) {
		b.add("leanify", "-q -p", b.leanifyIter(), "{in}")
	}
	b.peTail()
	return ""
}

// aslr reports whether a PE image is relocatable; unreadable images count as relocatable.
func aslr(path string) bool {
	f, err := pe.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	switch h := f.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		return h.DllCharacteristics&pe.IMAGE_DLLCHARACTERISTICS_DYNAMIC_BASE != 0
	case *pe.OptionalHeader64:
		return h.DllCharacteristics&pe.IMAGE_DLLCHARACTERISTICS_DYNAMIC_BASE != 0
	}
	return true
}

func (b *builder) dll() string {
	switch {
	case !b.s.Exe.Enabled:
		return disabled
	case detect.IsSigned(b.path):
		return "signed executable"
	case detect.HasPEOverlay(b.path):
		return overlay
	}
	b.peTail()
	return ""
}

// The tools drop data appended to a PE image, where PyInstaller and .NET single-file apps keep their payload.
const overlay = "executable with appended data"

func (b *builder) peTail() {
	// PETrim's default only strips relocations from images without ASLR.
	if b.s.Exe.PETrim {
		b.add("petrim", "{in}")
	}
	b.add("strip", "--strip-all -o {out} {in}")
	if b.s.Exe.UPX {
		b.add("upx", "--no-backup --force", upxFlags(b.l), "{in}")
	}
}
