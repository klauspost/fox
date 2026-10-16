package tools

// All lists every known tool, keyed by Name.
//
// Extra Files were found by inspecting PE imports and embedded strings: vcruntime140_1.dll is imported by
// gsdll64.dll, mutool, pngwolf, qpdf (+qpdf30.dll); gswin64c loads gsdll64.dll at runtime;
// m7zRepacker reads its .ini, which points it at 7z.exe next to itself.
// Not bundled, expected from the system: vcruntime140.dll/msvcp140.dll/vcomp140.dll (VC++ 2015+ redist, see EnsureRuntime),
// msvcr90.dll for mp3packer (VC++ 2008 redist) and OpenCL.dll for guetzli.
var All = index([]Tool{
	{"leanify", []string{"Leanify.exe"}, "leanify", "https://github.com/JayXon/Leanify"},
	{"ect", []string{"ECT.exe"}, "ect", "https://github.com/fhanau/Efficient-Compression-Tool"},
	{"oxipng", []string{"oxipng.exe"}, "oxipng", "https://github.com/oxipng/oxipng"},
	{"pingo", []string{"pingo.exe"}, "", "https://css-ig.net/pingo"},
	{"truepng", []string{"TruePNG.exe"}, "", "http://x128.ho.ua/pngutils.html"},
	{"pngoptimizer", []string{"PngOptimizer.exe"}, "", "https://psydk.org/pngoptimizer"},
	{"pngout", []string{"pngout.exe"}, "pngout", "http://advsys.net/ken/utils.htm"},
	{"pngwolf", []string{"pngwolf.exe", "vcruntime140_1.dll"}, "pngwolf", "https://github.com/jibsen/pngwolf-zopfli"},
	{"pngrewrite", []string{"pngrewrite.exe"}, "pngrewrite", "https://entropymine.com/jason/pngrewrite/"},
	{"pngquant", []string{"pngquant.exe"}, "pngquant", "https://pngquant.org"},
	{"apngopt", []string{"apngopt.exe"}, "apngopt", "https://sourceforge.net/projects/apng/"},
	{"advpng", []string{"advpng.exe"}, "advpng", "https://www.advancemame.it/comp-readme"},
	{"advdef", []string{"advdef.exe"}, "advdef", "https://www.advancemame.it/comp-readme"},
	{"advzip", []string{"advzip.exe"}, "advzip", "https://www.advancemame.it/comp-readme"},
	{"deflopt", []string{"DeflOpt.exe"}, "", "https://web.archive.org/web/2018/http://www.walbeehm.com/download/"},
	{"defluff", []string{"defluff.exe"}, "defluff", "https://encode.su/threads/1214-defluff-a-deflate-huffman-optimizer"},
	{"jpegoptim", []string{"jpegoptim.exe"}, "jpegoptim", "https://github.com/tjko/jpegoptim"},
	{"jpegtran", []string{"jpegtran.exe"}, "jpegtran", "https://libjpeg-turbo.org"},
	{"mozjpegtran", []string{"mozjpegtran.exe"}, "mozjpegtran", "https://github.com/mozilla/mozjpeg"},
	{"jhead", []string{"jhead.exe"}, "jhead", "https://www.sentex.ca/~mwandel/jhead/"},
	{"jpeg-recompress", []string{"jpeg-recompress.exe"}, "jpeg-recompress", "https://github.com/danielgtaylor/jpeg-archive"},
	{"cjpegli", []string{"cjpegli.exe"}, "cjpegli", "https://github.com/google/jpegli"},
	{"guetzli", []string{"guetzli.exe"}, "guetzli", "https://github.com/google/guetzli"},
	{"gifsicle", []string{"gifsicle.exe"}, "gifsicle", "https://www.lcdf.org/gifsicle/"},
	{"cwebp", []string{"cwebp.exe"}, "cwebp", "https://developers.google.com/speed/webp"},
	{"magick", []string{"magick.exe"}, "magick", "https://imagemagick.org"},
	{"imagew", []string{"imagew.exe"}, "imagew", "https://entropymine.com/imageworsener/"},
	{"mutool", []string{"mutool.exe", "vcruntime140_1.dll"}, "mutool", "https://mupdf.com"},
	{"gs", []string{"gswin64c.exe", "gsdll64.dll", "vcruntime140_1.dll"}, "gs", "https://www.ghostscript.com"},
	{"cpdf", []string{"cpdf.exe"}, "cpdf", "https://www.coherentpdf.com"},
	{"qpdf", []string{"qpdf.exe", "qpdf30.dll", "vcruntime140_1.dll"}, "qpdf", "https://qpdf.sourceforge.io"},
	{"minify", []string{"minify.exe"}, "minify", "https://github.com/tdewolff/minify"},
	// libdeflate's gzip: "-12" is not understood by GNU gzip (also on Windows PATH via Git), so PATH's gzip must not be used.
	{"gzip", []string{"gzip.exe"}, "", "https://github.com/ebiggers/libdeflate"},
	{"zrecompress", []string{"zRecompress.exe"}, "", "https://nikkhokkho.sourceforge.io/static.php?page=zRecompress"},
	{"m7zrepacker", []string{"m7zRepacker.exe", "m7zRepacker.ini", "7z.exe", "7z.dll"}, "", "https://encode.su/threads/2273-m7zRepacker"},
	{"sqlite3", []string{"sqlite3.exe"}, "sqlite3", "https://sqlite.org"},
	{"flac", []string{"flac.exe", "libFLAC.dll"}, "flac", "https://xiph.org/flac/"},
	{"flacout", []string{"flacout.exe"}, "", "http://www.hydrogenaud.io/forums/index.php?showtopic=106545"},
	{"shntool", []string{"shntool.exe"}, "shntool", "http://shnutils.freeshell.org/shntool/"},
	{"mp3packer", []string{"mp3packer.exe"}, "mp3packer", "https://hydrogenaud.io/index.php/topic,32379.0.html"},
	{"optivorbis", []string{"optivorbis.exe"}, "optivorbis", "https://github.com/OptiVorbis/OptiVorbis"},
	{"rehuff_theora", []string{"rehuff_theora.exe"}, "", "https://theora.org"},
	{"ffmpeg", []string{"ffmpeg.exe"}, "ffmpeg", "https://ffmpeg.org"},
	{"mkclean", []string{"mkclean.exe"}, "mkclean", "https://www.matroska.org/downloads/mkclean.html"},
	{"mp4file", []string{"mp4file.exe"}, "mp4file", "https://github.com/enzo1982/mp4v2"},
	{"strip", []string{"strip.exe"}, "strip", "https://www.gnu.org/software/binutils/"},
	{"upx", []string{"upx.exe"}, "upx", "https://upx.github.io"},
	{"petrim", []string{"PETrim.exe"}, "", "https://www.codeproject.com/Articles/12532/PE-Trim"},
})

func index(ts []Tool) map[string]Tool {
	m := make(map[string]Tool, len(ts))
	for _, t := range ts {
		m[t.Name] = t
	}
	return m
}
