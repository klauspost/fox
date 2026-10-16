# fox — File Optimizer X

fox makes files smaller without changing what they contain. It runs a chain of established external optimizers
(oxipng, ECT, Leanify, jpegtran, gifsicle, qpdf, ffmpeg, …) over each file and keeps a result only if it is smaller.
Several files are processed at once, one per physical CPU core by default. It has a command line and a local web UI.

fox is a Go rewrite of [FileOptimizer](https://nikkhokkho.sourceforge.io/static.php?page=FileOptimizer) by
**Javier Gutiérrez Chamorro**. His years of work picking and tuning these tools is what fox is built on.
If fox saves you space, please consider
**[donating to him](https://nikkhokkho.sourceforge.io/images/FileOptimizerDonate.html)**.

## Lossless by default, lossy is opt-in

With default settings, the decoded content of a file never changes: images decode to the same pixels, audio and
video to the same samples and frames. Steps that can change it only run when you enable them:

- `jpeg.lossy`, `png.lossy`, `gif.lossy`, `webp.lossy` (or `-lossy` for all four) allow lossy image steps.
- `pdf.profile` other than `none` re-renders PDFs with Ghostscript and lets qpdf recompress images as JPEG.
- `audio.trimSilence` cuts silence from FLAC and WAV.
- Minifying HTML, CSS and JavaScript/JSON is off by default. SVG minification is on; SVGs with text are skipped.

By default fox does strip metadata: EXIF, XMP, comments, text chunks, sRGB colour profiles, audio/video tags. Turn
on `keepMetadata` for a format (or use `-keep-metadata`) to keep it. Metadata that changes how an image looks is
never stripped; such images are handled as if `keepMetadata` were on:

- **Rotated images:** a JPEG, PNG or WebP with an EXIF Orientation other than 1.
- **Colour-managed images:** an ICC profile that is not sRGB (judged by its colorants and tone curves, not its name)
  in JPEG, PNG, WebP, TIFF, BMP or GIF; PNG `cICP`, or `gAMA`/non-sRGB `cHRM` without an `sRGB` chunk.

PNG optimizers may also change the colour values of fully transparent pixels, which are invisible.

## Usage

```
fox [flags] [files or folders...]
```

Files are optimized **in place**. Wildcards such as `*.png` are expanded by fox itself (case-insensitively on
Windows), so they also work in `cmd.exe` and PowerShell. Flags may come before or after the files; `--` ends the
flags.

```
$ fox -level 3 photos logo.png fox.svg
   -4.2%       334 B →      320 B    0.1s  fox.svg
  -21.7%   242.56 KB →  189.91 KB    0.6s  photos\logo.jpg
  -33.5%    28.94 KB →   19.24 KB   12.9s  logo.png
    0.0%    22.34 KB →   22.34 KB   15.8s  photos\screen.png

4 done, 0 skipped, 0 failed. Saved 62.37 KB of 294.17 KB (21.2%) in 15.8s.
```

| Flag             | Effect                                                                     |
|------------------|----------------------------------------------------------------------------|
| `-level n`       | 1 (fast) … 9 (extreme). Higher levels try harder.                          |
| `-j n`           | Files processed concurrently (0 = one per physical CPU core).              |
| `-lossy`         | Allow lossy optimization of JPEG, PNG, GIF and WebP.                       |
| `-keep-metadata` | Keep metadata in all formats.                                              |
| `-no-cache`      | Also process files fox has already optimized.                              |
| `-v`             | Print every step, skipped files with the reason, and notes.                |
| `-no-browser`    | Web UI: only print the URL, don't open a browser.                          |
| `-version`       | Print the version.                                                         |

Flags override the settings file for this run only and are never saved. They only apply to files given on the
command line; for the web UI, use its settings panel.

**Folders** are processed recursively, but only files with a known extension (including `web.jsExtensions`). fox
skips version-control, package-cache and tool folders, ignoring case: `.git`, `.svn`, `.hg`, `.bzr`, `.yarn`,
`.pnpm`, `node_modules/.cache`, `_cacache`, `__pycache__` and `fox-tools`. This also applies when such a folder is
named on the command line or matched by a wildcard such as `dir\*`. Files named explicitly are detected by content,
even without a known extension.

Ctrl+C cancels running jobs; a second Ctrl+C kills fox. Exit code is 0 on success, 1 if any file failed or was not
found, 2 on invalid flags and 130 when interrupted.

### Web UI

Run `fox` without files to start the web UI. It listens on `127.0.0.1` only, behind a random URL token, and opens
your browser. The URL is also printed, so you can paste it if the browser does not open.

- **Add files**, **Add folder** (native dialog) and **paste a path** optimize files **in place**, like the command
  line.
- **Drag and drop** uploads the files to fox, which optimizes the copies. In browsers with the File System Access
  API (Chrome, Edge) fox writes the result back over the dropped original after you grant write access, unless the
  original changed since the drop. Otherwise each result is offered as a download. Dropped folders work too.
- All settings are in the settings panel and saved to the settings file immediately.

The server exits on Ctrl+C, or once the browser tab has been closed for 30 seconds and no jobs are left.

On Linux and macOS, fox hands the browser a short-lived private HTML file (mode 0600 in a `fox-open-*` temp folder,
deleted after about 30 seconds and on exit) that redirects to the tokenized URL, so the token never appears on a
process command line other users can read. On Windows the URL is passed directly. Snap-packaged browsers on Linux
(e.g. Ubuntu's default Firefox) cannot read files in `/tmp` and show "file not found"; paste the printed URL instead.
On Linux the file dialogs need `zenity`, `qarma` or `matedialog`.

## Supported formats

Tools run in the listed order. Each step works on the best result so far, and a result is kept only if it is
smaller. *lossy*: only with lossy enabled for the format. *strip*: only when metadata is not kept.

| Format | Extensions | Tools |
|---|---|---|
| JPEG | .jpg .jpeg .jpe .jfif .jfi .jif | pingo *(strip)*, cjpegli *(lossy, strip)*, guetzli *(lossy, strip)*, jpeg-recompress *(lossy)*, jhead *(strip, not CMYK)*, Leanify, ImageMagick *(lossy)*, jpegoptim, jpegtran, mozjpegtran, ECT |
| PNG / APNG | .png .apng | apngopt *(APNG)*, pngquant *(lossy, strip)*, PngOptimizer *(strip)*, TruePNG, PNGOUT, oxipng, Leanify *(strip)*, pngwolf, pngrewrite *(strip)*, advpng *(strip)*, ECT, pingo, DeflOpt, defluff |
| ICO / CUR | .ico .cur | Leanify *(strip, uses `png.keepMetadata`)* |
| GIF | .gif | ImageMagick *(lossy)*, gifsicle |
| WebP | .webp | pingo, cwebp |
| BMP | .bmp .dib | ImageMagick, ImageWorsener *(not colour-managed)* |
| TIFF | .tif .tiff | ImageMagick *(strip)* |
| SVG | .svg | Leanify, minify |
| PDF | .pdf .ai | mutool, Ghostscript *(only with a `pdf.profile`, not for .ai)*, cpdf, qpdf |
| HTML ¹ | .html .htm | minify |
| CSS ¹ | .css | minify |
| JavaScript / JSON ¹ | .js .mjs .cjs .json | minify (JSON with `--json-keep-numbers`, so numbers keep their exact spelling) |
| ZIP based | .zip .zipx .jar .war .ear .epub .cbz .kmz .xps .oxps .ora .vsdx .3mf, Office (.docx .docm .dotx .dotm .xlsx .xlsm .xlsb .xltx .xltm .pptx .pptm .potx .potm .ppsx .ppsm), OpenDocument (.odt .ods .odp .odg .odf .odb) | Leanify *(strip)*, ECT *(strip)*, advzip *(strip)*, DeflOpt *(no directory entries)*, defluff |
| GZip | .gz .tgz .svgz | libdeflate gzip *(strip)*, Leanify *(strip)*, advdef, zRecompress *(strip)*, ECT *(strip)*, DeflOpt, defluff |
| TAR | .tar | Leanify *(only with `archive.recurse`)* |
| 7-Zip | .7z | m7zRepacker *(level 8–9)* |
| SQLite | .sqlite .sqlite3 .db | sqlite3 (`VACUUM INTO`) |
| FLAC | .flac | shntool *(with `audio.trimSilence`, strip)*, flac, flacout *(level 9, strip)* |
| WAV | .wav | shntool strip *(strip)*, shntool trim *(with `audio.trimSilence`, strip)* |
| MP3 | .mp3 | mp3packer ² |
| Ogg | .ogg .oga .ogv .opus .spx | Vorbis: OptiVorbis ². Theora video: ffmpeg remux, rehuff_theora |
| MP4 / QuickTime | .mp4 .m4a .m4v .m4b .mov .3gp .3g2 | ffmpeg remux, mp4file *(not fragmented MP4)* |
| Matroska / WebM | .mkv .mka .mk3d .webm | ffmpeg remux, mkclean *(not WebM, not with Matroska v4 elements)* |
| Executables ³ | .exe .dll .ocx .cpl | Leanify *(.exe without ASLR)*, PETrim, strip, UPX *(with `exe.upx`)* |

¹ Off by default (`web.html`, `web.css`, `web.js`): minifying source files changes them in ways you may not want.
Extensions in `web.jsExtensions` are minified as JavaScript even when `web.js` is off.<br>
² Kept only if ffmpeg decodes the result to exactly the same audio, so these steps need ffmpeg. Otherwise the file is
left unchanged with the note "decoded audio changed" (mp3packer drops the gapless info of MP3s written by ffmpeg;
OptiVorbis can make very short files 128 samples shorter in ffmpeg-based players).<br>
³ Off by default (`exe.enabled`).

More details:

- Files are identified by content as well as extension: a renamed PNG, JPEG, GIF, archive, … still gets the right
  tools, and a file whose content contradicts its extension is skipped. Ogg files are routed by their streams, so a
  Theora video named `.ogg` is treated as video.
- With metadata kept, some optimizers that would drop it are skipped (see *strip* above). WebP keeps it through
  `pingo -nostrip` and `cwebp -metadata all`; for a rotated WebP in lossy mode pingo is skipped. ZIP keeps comments
  and extra fields, GZip its name, comment and time.
- Android 9-patch images (`*.9.png`) skip the PNG tools that break their border, and lossy pingo.
- Video and audio remuxes drop only global tags; track language and title are kept. A remux result smaller than
  half the current size is rejected, and the ffmpeg remux fails on any demuxing error instead of keeping a truncated
  output.
- 7z repacking keeps empty folders, attributes and timestamps.
- `archive.recurse` also optimizes files inside archives (ZIP, GZip, TAR). Inner JPEGs always keep their EXIF, but
  inner PNGs may lose an eXIf orientation, inner SVGs are minified, and signed JARs and signed Office documents lose
  their signatures.

Files that are never touched, with the reason shown in the UI and with `-v`:

| Reason | Files |
|---|---|
| unsupported format | Signed packages: .apk .aab .ipa .appx .appxbundle .msix .msixbundle .xpi .crx .ipsw .vsix .nupkg; AVIF, HEIC and JPEG XL |
| read-only | Files without write permission |
| signed PDF, PDF/A document | PDFs with a signature or document timestamp (`/ByteRange`, `/DocMDP`), PDF/A |
| layered PDF | PDFs with layers, when `pdf.skipLayered` is on and a profile is used |
| signed executable, installer/self-extracting, executable with appended data | Authenticode-signed files, installers, PyInstaller, .NET single-file and similar |
| GeoTIFF, TIFF with Photoshop layers, unsupported TIFF | ImageMagick would drop these tags; BigTIFF |
| SVG with text | SVGs with `<text>`: minification removes spaces between `<tspan>`s |
| HTML with whitespace-sensitive content | `white-space: pre`, `pre-wrap`, `pre-line`, `break-spaces`, `<xmp>`, `<listing>`, `<plaintext>`, inline SVG `<text>` |
| ZIP with Unicode name fields | The tools drop the extra fields, which would change file names |
| multi-member gzip, damaged gzip | Concatenated or BGZF files |
| animated WebP | cwebp cannot read animations and pingo gains nothing |
| HDR gain map JPEG | Ultra HDR / ISO 21496-1 / Apple gain maps, which every JPEG tool drops |
| ICO with color-managed PNG | Leanify drops gamma/colour chunks from PNGs inside icons |
| unsupported Ogg stream (…) | Opus, FLAC, Speex and other non-Vorbis/Theora Ogg streams |
| database in use | SQLite files with a `-wal`, `-shm` or `-journal` file next to them |

## Settings

Settings live in `~/.fox/config.json` (`%USERPROFILE%\.fox\config.json` on Windows). The web UI edits this file;
missing keys use the defaults. If the file cannot be parsed, fox moves it to `config.json.bad`, warns
(`fox: warning: could not load settings: … (moved to config.json.bad, using defaults)`) and uses the defaults.

The defaults:

```json
{
  "level": 5,
  "workers": 0,
  "preserveTimestamps": true,
  "lowPriority": true,
  "skipOptimized": true,
  "include": null,
  "exclude": null,
  "disabledTools": null,
  "toolPaths": null,
  "jpeg": { "lossy": false, "keepMetadata": false, "arithmetic": false },
  "png": { "lossy": false, "keepMetadata": false },
  "gif": { "lossy": false, "keepMetadata": false },
  "webp": { "lossy": false, "keepMetadata": false },
  "tiff": { "keepMetadata": false },
  "bmp": { "keepMetadata": false },
  "pdf": { "profile": "none", "dpi": 150, "skipLayered": false },
  "audio": { "keepMetadata": false, "trimSilence": false },
  "video": { "keepMetadata": false },
  "archive": { "keepMetadata": false, "recurse": false },
  "web": { "html": false, "css": false, "js": false, "svg": true, "jsExtensions": null },
  "exe": { "enabled": false, "upx": false, "peTrim": true },
  "leanifyIterations": 0,
  "pngwolfIterations": 0
}
```

| Key | Meaning |
|---|---|
| `level` | 1 (fast) … 9 (extreme). |
| `workers` | Files processed concurrently; 0 = number of physical CPU cores (SMT threads are not counted). The steps for one file run one after another. |
| `preserveTimestamps` | Restore the modification time after writing the optimized file. |
| `lowPriority` | Run tools at below-normal priority, so the machine stays responsive. |
| `skipOptimized` | Skip files fox already processed with the same settings (hashes are kept in `~/.fox/cache`). |
| `include`, `exclude` | Glob patterns matched case-insensitively against file names, e.g. `["*.png"]`. |
| `disabledTools` | Tool names that never run, e.g. `["guetzli"]`. |
| `toolPaths` | Command used to run a tool, e.g. `{"pngout": ["wine", "/opt/fox/pngout.exe"]}`. Overrides the bundled copy. |
| `jpeg/png/gif/webp.lossy` | Allow steps that change pixels. |
| `*.keepMetadata` | Keep EXIF, ICC profiles, comments and tags. `png.keepMetadata` also applies to ICO; `audio.keepMetadata` applies to .m4a, .m4b and .mka. |
| `jpeg.arithmetic` | Arithmetic coding: smaller, but many decoders can't read it. |
| `pdf.profile` | `none` (lossless), or a lossy Ghostscript profile: `screen`, `ebook`, `printer`, `prepress`, `custom` (downsamples to `pdf.dpi`). Any profile other than `none` also lets qpdf recompress images as JPEG. |
| `pdf.skipLayered` | Leave PDFs with layers alone when a profile is used. |
| `audio.trimSilence` | Trim silence from FLAC and WAV. This changes the audio. |
| `archive.recurse` | Also optimize files inside archives (see above). |
| `web.html`, `web.css`, `web.js`, `web.svg` | Enable minification per type. `web.js` covers .js .mjs .cjs .json. |
| `web.jsExtensions` | Extra extensions of plain JavaScript files (not JSX or TypeScript), e.g. `[".jsm"]`. Minified even when `web.js` is off. |
| `exe.enabled`, `exe.upx`, `exe.peTrim` | Optimize executables / also use UPX / use PETrim. |
| `leanifyIterations`, `pngwolfIterations` | Override the iteration count derived from `level` when > 0. |

## How files are written

All tool work happens on a private copy in a temp folder: `%TEMP%\fox` on Windows; on Linux and macOS
`<user cache dir>/fox/tmp` (e.g. `~/.cache/fox/tmp`, `~/Library/Caches/fox/tmp`), or `<TMPDIR>/fox-<uid>` if
there is no cache folder or `HOME` belongs to another user (e.g. `sudo` keeping `HOME`). The folder is created with
mode 0700 and must be owned by the current user. Job folders older than 7 days are removed automatically.

When the result is smaller, the original is overwritten in place (write, truncate, sync). That keeps its creation
time, permissions, ACLs and hard links and needs no extra disk space. If the original changed while it was being
optimized, the result is discarded. If every step fails, the file is left unchanged and reported as done with the
error as a note; it is not recorded as already optimized.

## Building

Requires Go 1.26. The build system is [Task](https://taskfile.dev), run through `go tool`, so nothing else needs
installing.

```
go tool task release         # pack all tools and build bin/fox.exe
go tool task release-lite    # build bin/fox-lite.exe without ImageMagick and Ghostscript, then restore the full bundle
go tool task test            # go test ./...
go tool task bundle          # pack the tools (PLUGINS=<dir>, EXCLUDE=magick,gs)
go tool task build           # build bin/fox with the current bundle (OUT=bin/name, without .exe)
```

The tools come from FileOptimizer's `Plugins64` folder; the default source is `_ref/Win64/Debug/Plugins64`, set
`PLUGINS=<dir>` to use another. `build` alone does not pack the bundle. The packer (`go run ./internal/tools/pack`)
takes `-exclude <comma-separated tool names, no spaces>` and is incremental: unchanged files are reused, so switching
between the full and the lite bundle takes seconds. A build without a bundle works too and uses tools from `PATH`.

The lite build is about 22 MB smaller.

## Platforms

**Windows x64.** All tools are compressed into `fox.exe`. They are extracted on first use to
`<exe folder>\fox-tools\<bundle hash>\`, or `%LocalAppData%\fox\tools` if that is not writable. Each running fox
keeps a `fox.lock` open in its folder, so fox and fox-lite, or different fox versions, can run side by side; folders
no running fox uses are removed automatically.

Most bundled tools need the Microsoft Visual C++ 2015+ x64 runtime. If the tools are embedded and the runtime is
missing, fox downloads `https://aka.ms/vs/17/release/vc_redist.x64.exe` at startup (10-minute limit) and runs it
elevated (`/install /passive /norestart`, with a UAC prompt). The runtime counts as present if
`HKLM\SOFTWARE\Microsoft\VisualStudio\14.0\VC\Runtimes\X64` has `Installed=1`, or if `vcruntime140.dll`,
`msvcp140.dll` and `vcomp140.dll` are in System32. If the download or install fails, fox warns and continues. If you
decline the UAC prompt or cancel the installer, fox writes `%LocalAppData%\fox\vcredist-declined` and from then on
only prints a warning with the download URL; delete that file to be asked again. mp3packer also needs the
Visual C++ 2008 runtime and guetzli needs `OpenCL.dll`; neither is installed by fox.

**fox-lite** uses ImageMagick (`magick.exe`) and Ghostscript from the host if installed. Ghostscript is found on
`PATH` (`gswin64c.exe`) or in `%ProgramFiles%\gs\gs<version>\bin\gswin64c.exe` (newest version wins). `toolPaths`
still override this.

**Linux, macOS.** No tools are bundled. fox uses tools found in `PATH` under their usual names (`oxipng`, `ect`,
`leanify`, `jpegoptim`, `gifsicle`, `cwebp`, `magick`, `qpdf`, `gs`, `mutool`, `ffmpeg`, `flac`, `minify`, …).
Tools without a native port (pingo, TruePNG, PngOptimizer, DeflOpt, zRecompress, m7zRepacker, flacout,
rehuff_theora, PETrim) are never taken from `PATH`; run them through Wine with `toolPaths`, e.g.
`{"pingo": ["wine", "/opt/fox/pingo.exe"]}`. The same goes for libdeflate's gzip, since GNU gzip lacks level 12:
`{"gzip": ["libdeflate-gzip"]}`. Missing tools are skipped.

## What's different from FileOptimizer

**New**

- Processes many files concurrently.
- Command line and a browser UI instead of a Windows-only GUI; runs on Linux and macOS with host tools.
- Lossless by default in the strict sense: no step that can change decoded content runs unless enabled.
- SVG is a first-class format.
- Remembers optimized files and skips them next time.
- Originals are only touched once, with a smaller result, and never if they changed meanwhile.
- Many content checks that skip files the tools would damage (see the table above).

**Dropped, on purpose**

- Recycle bin and `.bak` backups, beep, shutdown when done, ads, update check, translations, statistics, always on
  top, startup delay, temp folder option, log levels, "allow duplicates", the process priority picker (now a single
  low-priority switch).
- Formats: PCX, TGA, SWF, MNG, JPEG 2000, OLE (doc/xls/msi), MIME, Lua, OBJ/LIB, Tencent QQ and the ImageMagick
  "misc" bucket. AVIF, HEIC and JPEG XL: ImageMagick re-encodes them lossily. DNG: tinydng re-encodes the embedded
  preview lossily and writes JPEG XL raw data. XML (.xml .xsl .xslt .kml .fb2 .gpx .rss .atom): minify changes CDATA
  and whitespace that are part of the content. XHTML: minify drops end tags and quotes an XML parser needs.
- Tools: csstidy (CSS 2.1 only; deletes `@layer`/`@container` blocks, nesting and `calc()` spacing), jsmin
  (truncates nested template literals), tidy and Leanify for HTML, and `jhead -autorot` (needs jpegtran on `PATH`;
  fox keeps the orientation tag instead).
- Signed or packaged archives (APK, IPA, MSIX, …), signed executables and signed PDFs are never modified, since
  that breaks their signatures.

**Fixed**

- WebP files were detected as WAV (both start with `RIFF`), and the WebP check used the wrong offset.
- Matroska/WebM content detection looked for the wrong signature.
- Every ISO-BMFF file was detected as AVIF by content, and the MP4 check used the wrong offset. fox tells AVIF/HEIC
  apart from video by brand.
- CMYK JPEG detection required both an `FFC0` and an `FFC2` marker, so it practically never matched and jhead
  corrupted CMYK JPEGs.
- Ogg files were always sent to OptiVorbis, which drops the video of Theora files named `.ogg`.
- The GIF ImageMagick step ran by default and requantizes frames with their own palette.
- TruePNG ran with `-g1`, which applies the gAMA chunk to the pixels.
- PNGOUT always used its slowest strategy regardless of level.
- mp4file kept only the `moov` of fragmented MP4s and dropped the fragments.
- mkclean dropped Matroska v4 elements (HDR colour, Opus `CodecDelay`), which changes playback.
- mp3packer could remove the gapless info of ffmpeg-written MP3s (now verified by decoding).
- DeflOpt with "keep metadata" dropped a gzip comment but kept its header flag, corrupting the file.
- The ECT strip flag for GZip was garbled (`-str?p`).
- jpeg-recompress was passed `--qualmeity` instead of `--quality`.
- ECT's `--strict` for JPEG followed the PNG lossy setting instead of the JPEG one.
- Ghostscript with the `none` profile always failed (`.setpdfwrite` no longer exists); it now only runs with a
  profile.
- jpegoptim arithmetic coding used a non-existent flag (`--with-arith`); UPX was passed `--crp-ms`, which UPX 5
  rejects; flac was passed `--keep-foreign-metadata`, which it rejects for FLAC input.
- SQLite databases are rebuilt with `VACUUM INTO`, which keeps `user_version`, `application_id` and WAL mode, which
  a dump and restore lost.

## License

fox is licensed under the [GNU Affero General Public License v3.0](LICENSE), like FileOptimizer.

The bundled tools are separate programs that keep their own licenses; their home pages are listed in
[`internal/tools/registry.go`](internal/tools/registry.go). Note that Ghostscript, MuPDF (mutool) and cpdf are
AGPL-licensed as well.
