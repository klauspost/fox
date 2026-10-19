# Third-party tools

fox runs external programs. On Windows they are bundled in `fox.exe`, zstd-compressed in
[`internal/tools/bundle`](internal/tools/bundle) (`core`, plus `extra` for the full build only). They were taken from
FileOptimizer's `Plugins64` folder. Each tool is a separate program under its own license; fox's own
[AGPL-3.0 license](LICENSE) does not change their terms.

Versions come from each program's version resource or its `--version`/usage output. "Not determinable" means the
program reports none. The licenses were researched from the tools' documentation, the strings in the binaries and the
projects' websites; where something could not be confirmed, the table says so.

**This file is authoritative.** Some license files that come with FileOptimizer's `Plugins64` are out of date: for
example `cpdf.txt` still says "non-commercial use only" although the bundled cpdf 2.8.1 is AGPL-3.0, `imagew.txt`
covers ImageWorsener 1.x under GPL-3.0 although 1.3.5 is MIT, and `strip.txt` contains GPL-2.0 although binutils
2.36.1 is GPL-3.0-or-later.

## Bundled tools

| Tool | Bundled files | Version | License | Copyright | Upstream |
|---|---|---|---|---|---|
| Leanify | Leanify.exe | 0.4.3 | MIT | JayXon | https://github.com/JayXon/Leanify |
| ECT | ECT.exe | 0.9.5 | Apache-2.0 | Felix Hanau | https://github.com/fhanau/Efficient-Compression-Tool |
| oxipng | oxipng.exe | 9.1.5 | MIT | oxipng contributors | https://github.com/oxipng/oxipng |
| TruePNG | TruePNG.exe | 0.6.2.5 | None published ([see below](#tools-without-a-published-license)) | x128 | http://x128.ho.ua/pngutils.html |
| PngOptimizer | PngOptimizer.exe | 2.7 (PngOptimizerCL) | GPL-2.0-or-later | Hadrien Nilsson | https://github.com/hadrien-psydk/pngoptimizer |
| PNGOUT | pngout.exe | Build of Feb 13 2015 | Freeware, non-commercial redistribution only ([see below](#pngout-and-flacout-ken-silverman)) | Ken Silverman | http://advsys.net/ken/utils.htm |
| pngwolf-zopfli | pngwolf.exe, vcruntime140_1.dll | 1.1.2 | GPL-3.0 | Bjoern Hoehrmann, Joergen Ibsen | https://github.com/jibsen/pngwolf-zopfli |
| pngrewrite | pngrewrite.exe | 1.4.0 | zlib | Jason Summers | https://entropymine.com/jason/pngrewrite/ |
| pngquant | pngquant.exe | 2.17.0 | GPL-3.0-or-later, plus the notice in its COPYRIGHT file | Kornel Lesiński and contributors | https://github.com/kornelski/pngquant |
| apngopt | apngopt.exe | 1.4 | zlib | Max Stepin | https://sourceforge.net/projects/apng/ |
| AdvanceCOMP | advpng.exe, advdef.exe, advzip.exe | 2.5 | GPL-3.0 | Andrea Mazzoleni | https://github.com/amadvance/advancecomp |
| DeflOpt | DeflOpt.exe | 2.07 | None published | Ben Jos Walbeehm | https://web.archive.org/web/2018/http://www.walbeehm.com/download/ |
| defluff | defluff.exe | 0.3.2 | None published | Joachim Henke | https://encode.su/threads/1214-defluff-a-deflate-huffman-optimizer |
| jpegoptim | jpegoptim.exe | 1.5.6 | GPL (v3-or-later upstream; the bundled 1.4.4 readme has v2) | Timo Kokkonen | https://github.com/tjko/jpegoptim |
| jpegtran | jpegtran.exe | IJG libjpeg 9b | IJG license | Thomas G. Lane, Guido Vollbeding (Independent JPEG Group) | https://ijg.org |
| mozjpegtran | mozjpegtran.exe | mozjpeg 4.1.5 | IJG, BSD-3-Clause, zlib | IJG, libjpeg-turbo and mozjpeg contributors | https://github.com/mozilla/mozjpeg |
| jhead | jhead.exe | 3.04 | Public domain | Matthias Wandel | https://www.sentex.ca/~mwandel/jhead/ |
| jpeg-recompress | jpeg-recompress.exe | 2.2.0 | MIT | Daniel G. Taylor; IQA: Tom Distler; SmallFry: Derek Buitenhuis | https://github.com/danielgtaylor/jpeg-archive |
| cjpegli | cjpegli.exe | Not determinable (built from libjxl) | BSD-3-Clause | The JPEG XL Project Authors | https://github.com/libjxl/libjxl |
| guetzli | guetzli.exe | 2.2.1 (CUDA/OpenCL fork) | Apache-2.0 | Google; fork by doterax | https://github.com/doterax/guetzli-cuda-opencl |
| gifsicle | gifsicle.exe | 1.97 | GPL-2.0-only | Eddie Kohler | https://www.lcdf.org/gifsicle/ |
| cwebp | cwebp.exe | 1.6.0 | BSD-3-Clause and the WebM patent grant | Google | https://chromium.googlesource.com/webm/libwebp |
| ImageMagick (full build only) | magick.exe | 7.1.2-0 Q16-HDRI | ImageMagick License; delegate libraries have their own | ImageMagick Studio LLC | https://imagemagick.org |
| ImageWorsener | imagew.exe | 1.3.5 | MIT; uses IJG libjpeg 9e | Jason Summers | https://entropymine.com/imageworsener/ |
| MuPDF (mutool) | mutool.exe, vcruntime140_1.dll | 1.26.2 | AGPL-3.0-or-later | Artifex Software | https://mupdf.com |
| Ghostscript (full build only) | gswin64c.exe, gsdll64.dll, vcruntime140_1.dll | 10.05.1 | AGPL-3.0 | Artifex Software | https://www.ghostscript.com |
| cpdf | cpdf.exe | 2.8.1 | AGPL-3.0 | Coherent Graphics | https://github.com/coherentgraphics/cpdf-source |
| qpdf | qpdf.exe, qpdf30.dll, vcruntime140_1.dll | 12.2.0 | Apache-2.0 | Jay Berkenbilt, Manfred Holger | https://github.com/qpdf/qpdf |
| minify | minify.exe | 2.20.19 | MIT | Taco de Wolff | https://github.com/tdewolff/minify |
| libdeflate gzip | gzip.exe | libdeflate 1.7 | MIT | Eric Biggers | https://github.com/ebiggers/libdeflate |
| zRecompress | zRecompress.exe | R2.12 | None published (source published) | Javier Gutiérrez Chamorro | https://nikkhokkho.sourceforge.io/static.php?page=zRecompress |
| m7zRepacker | m7zRepacker.exe, m7zRepacker.ini, 7z.exe, 7z.dll | m7zRepacker 1.0.32.319 (file version); 7-Zip 16.04 | m7zRepacker: none published. 7-Zip: LGPL-2.1-or-later, with the unRAR restriction for 7z.dll | Mario Scholz; 7-Zip: Igor Pavlov | https://encode.su/threads/2273-m7zRepacker, https://www.7-zip.org |
| sqlite3 | sqlite3.exe | 3.50.4 | Public domain | – | https://sqlite.org |
| FLAC | flac.exe, libFLAC.dll | 1.5.0 | flac.exe: GPL-2.0-or-later; libFLAC: BSD-3-Clause | Josh Coalson, Xiph.Org Foundation | https://xiph.org/flac/ |
| FLACOUT | flacout.exe | Build of Jul 3 2014 | Freeware, non-commercial redistribution only ([see below](#pngout-and-flacout-ken-silverman)) | Ken Silverman | http://advsys.net/ken/utils.htm |
| shntool | shntool.exe | 3.0.10 | GPL-2.0-or-later | Jason Jordan | http://shnutils.freeshell.org/shntool/ |
| mp3packer | mp3packer.exe | 2.04-268 | GPL-2.0-or-later | Reed "Omion" Wilson | https://hydrogenaud.io/index.php/topic,32379.0.html |
| OptiVorbis | optivorbis.exe | 0.3.0 | AGPL-3.0-only (the CLI) | Alejandro González | https://github.com/OptiVorbis/OptiVorbis |
| rehuff_theora | rehuff_theora.exe | Not determinable | Not confirmed; probably BSD-3-Clause, like Xiph's theora-exp it comes from | Not stated in the binary | https://wiki.xiph.org/Rehuff |
| FFmpeg | ffmpeg.exe | 8.0 (gyan.dev "essentials" build) | GPL-3.0 (built with `--enable-gpl --enable-version3`) | The FFmpeg developers and the authors of the linked libraries | https://ffmpeg.org |
| mkclean | mkclean.exe | 0.9.0 | BSD | Matroska Foundation | https://www.matroska.org/downloads/mkclean.html |
| MP4v2 (mp4file) | mp4file.exe | MP4v2 2.0.0, built from FileOptimizer's patched source | MPL-1.1 | Cisco Systems and other MP4v2 contributors | https://github.com/enzo1982/mp4v2 |
| GNU strip | strip.exe | GNU binutils 2.36.1 | GPL-3.0-or-later | Free Software Foundation | https://www.gnu.org/software/binutils/ |
| UPX | upx.exe | 5.2.0 | GPL-2.0-or-later with the UPX exception | Markus F.X.J. Oberhumer, László Molnár, John F. Reiser | https://upx.github.io |
| PETrim | PETrim.exe | 4.0.0.0 beta per its banner (version resource says 3.0.0.0) | Freeware, no published terms | Bitsum Technologies, Jeremy Collake | https://bitsum.com/other/ |
| Visual C++ runtime | vcruntime140_1.dll | 14.29.30038.0 | Microsoft Visual C++ Redistributable ([see below](#microsoft-visual-c-runtime)) | Microsoft | https://learn.microsoft.com/cpp/windows/latest-supported-vc-redist |

**Not bundled: pingo.** Its license (https://css-ig.net/pingo) forbids redistribution, so fox only uses a copy you
installed: `pingo.exe` on `PATH` on Windows, or a `toolPaths` entry on any system.

Programs built on libjpeg (jpegtran, mozjpegtran, ImageWorsener, jpegoptim, Leanify, ECT, jpeg-recompress and
others): this software is based in part on the work of the Independent JPEG Group.

## Source code for GPL, AGPL and MPL tools

These tools are distributed in binary form under licenses that require the corresponding source to be available.
fox does not mirror these sources; they are published by the upstream projects at the links below. Get the release
matching the version in the table above.

| Tool | Version | License | Source |
|---|---|---|---|
| FFmpeg | 8.0 | GPL-3.0 | https://ffmpeg.org/download.html#releases. The binary is the gyan.dev "essentials" build (https://www.gyan.dev/ffmpeg/builds/), which also statically links GPL and LGPL libraries (x264, x265, Xvid and others, see `ffmpeg -version`); their sources are published by those projects. |
| gifsicle | 1.97 | GPL-2.0-only | https://github.com/kohler/gifsicle |
| pngquant | 2.17.0 | GPL-3.0-or-later | https://github.com/kornelski/pngquant |
| pngwolf-zopfli | 1.1.2 | GPL-3.0 | https://github.com/jibsen/pngwolf-zopfli |
| PngOptimizer | 2.7 | GPL-2.0-or-later | https://github.com/hadrien-psydk/pngoptimizer |
| AdvanceCOMP | 2.5 | GPL-3.0 | https://github.com/amadvance/advancecomp/releases |
| jpegoptim | 1.5.6 | GPL | https://github.com/tjko/jpegoptim/releases |
| FLAC | 1.5.0 | GPL-2.0-or-later | https://github.com/xiph/flac/releases |
| shntool | 3.0.10 | GPL-2.0-or-later | http://shnutils.freeshell.org/shntool/ |
| mp3packer | 2.04 | GPL-2.0-or-later | Its original site is offline. We have not found an official source download for 2.04; the hydrogenaud.io thread above is the project's remaining home. |
| GNU strip | binutils 2.36.1 | GPL-3.0-or-later | https://ftp.gnu.org/gnu/binutils/ |
| UPX | 5.2.0 | GPL-2.0-or-later + exception | https://github.com/upx/upx/releases |
| MuPDF (mutool) | 1.26.2 | AGPL-3.0-or-later | https://mupdf.com/releases |
| Ghostscript | 10.05.1 | AGPL-3.0 | https://github.com/ArtifexSoftware/ghostpdl-downloads/releases |
| cpdf | 2.8.1 | AGPL-3.0 | https://github.com/coherentgraphics/cpdf-source |
| OptiVorbis | 0.3.0 | AGPL-3.0-only | https://github.com/OptiVorbis/OptiVorbis/releases |
| 7-Zip (7z.exe, 7z.dll) | 16.04 | LGPL-2.1-or-later | https://sourceforge.net/projects/sevenzip/files/7-Zip/16.04/ |
| MP4v2 (mp4file) | 2.0.0 | MPL-1.1 | Built from the patched copy in FileOptimizer's source tree (`Source/mp4v2`), not from an upstream release. FileOptimizer's author publishes the release sources in the downloads at https://sourceforge.net/projects/nikkhokkho/ and the source tree in SVN at https://sourceforge.net/p/nikkhokkho/code/HEAD/tree/trunk/FileOptimizer/. Upstream MP4v2: https://github.com/enzo1982/mp4v2. |

## PNGOUT and FLACOUT (Ken Silverman)

PNGOUT and FLACOUT are by **Ken Silverman, http://advsys.net/ken**. They are closed-source freeware. Their terms
(http://advsys.net/ken/utils.htm, written for PNGOUT and KZIP; FLACOUT's license link points to the same page)
permit redistribution **for non-commercial projects only**, and require Ken Silverman's name and website to be
clearly displayed when bundling. Commercial bundling needs permission; the page directs such requests to his
business partner, David Blake.

fox's AGPL-3.0 license allows anyone to sell fox, but that permission does not extend to these two programs. If you
redistribute fox commercially, remove them from the bundle or obtain permission. FileOptimizer's `pngout.txt` mentions a permission from Ardfry Imaging; that was
given to FileOptimizer, not to fox.

## Tools without a published license

These bundled programs come with a copyright notice but no license terms that grant redistribution. By default that
means all rights reserved. They are included because FileOptimizer and similar projects have shipped them for years,
not because a license permits it.

| Tool | What is known |
|---|---|
| TruePNG | Binary and website only say "by x128 (2010-2017)" / "x128 © 2009-2017". |
| DeflOpt | "Copyright (C) 2003-2007 by Ben Jos Walbeehm"; no license text. The author's site is gone. |
| defluff | "Copyright (c) 2010-2011 Joachim Henke"; released as binaries on encode.su without a license. |
| PETrim | Bitsum lists PESuite as "Freeware" and says each project is licensed separately; no terms were found. |
| m7zRepacker | "(C) 2010-2017 Mario Scholz"; its readme calls it a special version for members of the encode.ru forum. The 7-Zip files it uses are LGPL (see above). |
| zRecompress | "(c) Copyright 2012 by Javier Gutierrez Chamorro", the FileOptimizer author. Source is published, but no license is stated. |
| rehuff_theora | No copyright or license in the binary and no documentation. It comes from Xiph's theora-exp, which is probably BSD-licensed, but this is not confirmed. |

## Microsoft Visual C++ runtime

`vcruntime140_1.dll` (version 14.29.30038.0) is part of the Microsoft Visual C++ Redistributable. It is bundled
unmodified because pngwolf, mutool, Ghostscript and qpdf import it, and may be redistributed with an application that
needs it under the Visual Studio license terms for distributable code. It is not offered separately. The rest of
the runtime (`vcruntime140.dll`, `msvcp140.dll`, `vcomp140.dll`) is not bundled; fox installs Microsoft's
redistributable package when it is missing (see the README).
