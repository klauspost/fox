const $ = (sel, el = document) => el.querySelector(sel);

function h(tag, props = {}, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k in el) el[k] = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  el.append(...kids.flat().filter(k => k != null && k !== false));
  return el;
}

const icon = d => {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 16 16');
  svg.setAttribute('aria-hidden', 'true');
  svg.innerHTML = `<path d="${d}"/>`;
  return svg;
};
const ICON = {
  x: 'M4 4l8 8M12 4l-8 8',
  stop: 'M4.5 4.5h7v7h-7Z',
  down: 'M8 2.5v8M4.5 7 8 10.5 11.5 7M3 13.5h10',
  back: 'M6 3.5 2.5 7 6 10.5M2.5 7H10a3.5 3.5 0 0 1 0 7H8',
};

async function api(path, opts = {}) {
  const r = await fetch(path, opts);
  if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
  return r.status === 204 ? null : r.json();
}
const post = (path, body = {}) =>
  api(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });

let toastTimer;
function toast(msg, error = false) {
  const t = $('#toast');
  t.textContent = msg;
  t.classList.toggle('error', error);
  t.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove('show'), error ? 6000 : 3000);
}

function bytes(n) {
  if (!n) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.min(Math.floor(Math.log(Math.abs(n)) / Math.log(1024)), u.length - 1);
  const v = n / 1024 ** i;
  return `${i ? v.toFixed(v < 10 ? 2 : v < 100 ? 1 : 0) : v} ${u[i]}`;
}
const pct = (orig, size) => (orig > 0 ? (1 - size / orig) * 100 : 0);
const splitPath = p => {
  const i = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'));
  return [p.slice(0, i + 1), p.slice(i + 1)];
};

/* ---------- state ---------- */

const jobs = new Map();     // id -> latest engine event
const rows = new Map();     // id -> <tr>
const uploads = new Map();  // id -> { file, handle, root, rel, wb, err }
const canWriteBack = 'FileSystemFileHandle' in window && 'createWritable' in FileSystemFileHandle.prototype;

const tbody = $('#rows');
const pendingBody = $('#pending');

function refreshEmpty() {
  const empty = rows.size === 0 && pendingBody.children.length === 0;
  $('#empty').hidden = !empty;
  $('#table').hidden = empty;
}

/* ---------- rows ---------- */

const STATE_TEXT = { queued: 'Queued', running: 'Starting…', skipped: 'Skipped', failed: 'Failed' };

const skeleton = () => h('tr', {},
  h('td', { class: 'c-name' }), h('td', { class: 'c-fmt' }), h('td', { class: 'c-num num' }),
  h('td', { class: 'c-num num' }), h('td', { class: 'c-gain' }), h('td', { class: 'c-status' }), h('td', { class: 'c-act' }));

function rowFor(id) {
  let tr = rows.get(id);
  if (!tr) {
    tr = skeleton();
    rows.set(id, tr);
    tbody.append(tr);
  }
  return tr;
}

function nameCell(full, upload) {
  const [dir, base] = splitPath(full);
  return h('div', { class: 'name', title: full },
    h('span', { class: 'dot' }),
    h('span', { class: 'base' }, base),
    upload ? h('span', { class: 'tag' }, 'upload') : h('span', { class: 'dir' }, h('bdi', {}, dir)));
}

function gainCell(orig, size, show) {
  const g = h('span', { class: 'gauge' }, h('i'));
  g.firstChild.style.setProperty('width', `${orig > 0 ? Math.max(0, Math.min(100, (size / orig) * 100)) : 100}%`);
  const p = show ? pct(orig, size) : 0;
  return h('div', { class: 'gain' }, g,
    h('span', { class: `pct${p > 0.05 ? '' : ' zero'}` }, show ? (p > 0.05 ? `−${p.toFixed(1)}%` : '0%') : ''));
}

function render(e) {
  const tr = rowFor(e.id);
  const u = uploads.get(e.id);
  const saved = e.size < e.original;
  const finished = e.state === 'done' || e.state === 'skipped' || e.state === 'failed';
  tr.dataset.state = e.state;
  const [cName, cFmt, cOrig, cSize, cGain, cStatus, cAct] = tr.children;

  const shown = u?.rel ?? e.path;
  if (cName.dataset.key !== shown) {
    cName.dataset.key = shown;
    cName.replaceChildren(nameCell(u?.rel ?? e.path, e.upload));
  }
  cFmt.replaceChildren(e.format ? h('span', { class: 'fmt' }, e.format) : '—');
  cOrig.textContent = e.original ? bytes(e.original) : '';
  cSize.textContent = e.state === 'queued' || !e.original ? '' : bytes(e.size);
  cSize.classList.toggle('dim', !saved);
  cGain.replaceChildren(gainCell(e.original, e.size, e.state === 'running' || e.state === 'done'));

  let status;
  if (e.state === 'running') status = [h('span', { class: 'step' }, e.step || STATE_TEXT.running)];
  else if (e.state === 'done') status = [h('span', { class: saved ? 'ok' : '' }, saved ? writeBackText(u) : 'Already optimal')];
  else status = [h('span', {}, e.message || STATE_TEXT[e.state])];
  if (finished && e.elapsed) status.push(h('span', { class: 'time' }, `${(e.elapsed / 1e9).toFixed(1)}s`));
  cStatus.replaceChildren(h('div', { class: 'status', title: e.message || e.step || '' }, status));

  const act = h('span', { class: 'row-act' });
  if (!finished) {
    act.append(h('button', { type: 'button', title: 'Cancel', 'aria-label': 'Cancel', onclick: () => post('api/cancel', { id: e.id }).catch(err => toast(err.message, true)) }, icon(ICON.stop)));
  } else {
    if (e.upload && e.state === 'done' && saved) {
      if (u?.handle && (u.wb === 'needs' || u.wb === 'failed'))
        act.append(h('button', { type: 'button', class: 'wb', title: 'Write the optimized file back to the original', onclick: () => writeBack(e.id, true) }, icon(ICON.back)));
      if (u?.wb !== 'written')
        act.append(h('a', { href: `api/download/${e.id}`, download: splitPath(e.path)[1], title: 'Download optimized file' }, icon(ICON.down)));
    }
    act.append(h('button', { type: 'button', title: 'Remove from list', 'aria-label': 'Remove', onclick: () => post('api/remove', { id: e.id }).catch(err => toast(err.message, true)) }, icon(ICON.x)));
  }
  cAct.replaceChildren(act);
}

function writeBackText(u) {
  if (!u) return 'Optimized';
  switch (u.wb) {
    case 'writing': return 'Writing back…';
    case 'written': return 'Written back';
    case 'needs': return 'Ready to write back';
    case 'failed': return `Write back failed: ${u.err}`;
    default: return u.handle ? 'Optimized' : 'Ready to download';
  }
}

function upsert(e) {
  jobs.set(e.id, e);
  render(e);
  if (e.state === 'done' && uploads.has(e.id)) writeBack(e.id);
}

/* ---------- totals ---------- */

function renderStats(s) {
  const saved = s.original - s.size;
  const finished = s.done + s.skipped + s.failed;
  $('#t-saved').textContent = bytes(saved);
  $('#t-pct').replaceChildren(pct(s.original, s.size).toFixed(1), h('small', {}, '%'));
  $('#t-files').replaceChildren(String(finished), h('small', {}, ` / ${s.total}`));
  const st = $('#t-state');
  if (s.running) st.replaceChildren(h('b', {}, `${s.running} running`), ` · ${s.queued} queued`);
  else if (s.queued) st.replaceChildren(h('b', {}, `${s.queued} queued`));
  else if (s.total) st.replaceChildren(h('b', {}, 'Finished'), s.failed ? ` · ${s.failed} failed` : '');
  else st.textContent = 'Idle';
  const w = n => `${s.total ? (n / s.total) * 100 : 0}%`;
  for (const [cls, n] of [['p-done', s.done], ['p-skip', s.skipped], ['p-fail', s.failed], ['p-run', s.running]])
    $(`.progress .${cls}`).style.setProperty('width', w(n));
  document.title = s.running || s.queued ? `fox · ${finished}/${s.total}` : 'fox · File Optimizer X';
}

/* ---------- live updates ---------- */

function connect() {
  const es = new EventSource('api/events');
  let offlineTimer;
  es.addEventListener('snapshot', ev => {
    const list = JSON.parse(ev.data);
    const keep = new Set(list.map(j => j.id));
    for (const [id, tr] of rows) {
      if (!keep.has(id)) { tr.remove(); rows.delete(id); jobs.delete(id); uploads.delete(id); }
    }
    for (const j of list) upsert(j);
    refreshEmpty();
    updateWriteBackButton();
  });
  es.addEventListener('job', ev => { upsert(JSON.parse(ev.data)); refreshEmpty(); });
  es.addEventListener('stats', ev => renderStats(JSON.parse(ev.data)));
  es.onopen = () => { clearTimeout(offlineTimer); $('#offline').hidden = true; };
  es.onerror = () => {
    clearTimeout(offlineTimer);
    offlineTimer = setTimeout(() => { $('#offline').hidden = es.readyState === EventSource.OPEN; }, 2500);
  };
}

/* ---------- adding files ---------- */

async function pick(folder) {
  const btns = [...document.querySelectorAll(folder ? '#add-folder,[data-pick=folder]' : '#add-files,[data-pick=files]')];
  btns.forEach(b => (b.disabled = true));
  try {
    const { ids } = await post('api/pick', { folder });
    if (ids.length > 1) toast(`Added ${ids.length} files`);
  } catch (err) {
    toast(err.message, true);
  } finally {
    btns.forEach(b => (b.disabled = false));
  }
}

$('#add-files').addEventListener('click', () => pick(false));
$('#add-folder').addEventListener('click', () => pick(true));
for (const b of document.querySelectorAll('[data-pick]')) b.addEventListener('click', () => pick(b.dataset.pick === 'folder'));

$('#path-form').addEventListener('submit', async ev => {
  ev.preventDefault();
  const input = $('#path-input');
  const paths = input.value.split(/\r?\n/).filter(p => p.trim());
  if (!paths.length) return;
  try {
    const { ids } = await post('api/add', { paths });
    toast(ids.length ? `Added ${ids.length} file${ids.length > 1 ? 's' : ''}` : 'Nothing new to add');
    input.value = '';
    input.blur();
  } catch (err) {
    toast(err.message, true);
  }
});

$('#stop').addEventListener('click', () => post('api/cancel', { id: 0 }).catch(err => toast(err.message, true)));
$('#clear').addEventListener('click', () => post('api/clear').catch(err => toast(err.message, true)));

/* ---------- drag & drop ---------- */

let dragDepth = 0;
const hasFiles = ev => [...(ev.dataTransfer?.types ?? [])].includes('Files');
addEventListener('dragenter', ev => { if (hasFiles(ev)) { dragDepth++; $('#overlay').hidden = false; } });
addEventListener('dragleave', ev => { if (hasFiles(ev) && --dragDepth <= 0) { dragDepth = 0; $('#overlay').hidden = true; } });
addEventListener('dragover', ev => { if (hasFiles(ev)) { ev.preventDefault(); ev.dataTransfer.dropEffect = 'copy'; } });
addEventListener('drop', ev => {
  if (!hasFiles(ev)) return;
  ev.preventDefault();
  dragDepth = 0;
  $('#overlay').hidden = true;
  collect(ev.dataTransfer).then(found => {
    if (!found.length) return toast('No files found in drop', true);
    if (found.length > 1) toast(`Uploading ${found.length} files`);
    for (const f of found) enqueueUpload(f);
  }, err => toast(err.message, true));
});

// collect resolves dropped items to files. Handles and entries must be requested synchronously
// inside the drop event, because the DataTransfer is emptied once the handler returns.
async function collect(dt) {
  const items = [...dt.items].filter(i => i.kind === 'file');
  const useHandles = canWriteBack && items.length > 0 && 'getAsFileSystemHandle' in items[0];
  const handles = useHandles ? items.map(i => i.getAsFileSystemHandle()) : [];
  const entries = items.map(i => i.webkitGetAsEntry?.() ?? null);
  const files = [...dt.files];
  const out = [];

  const roots = [];
  for (const n of items.keys()) {
    const hd = useHandles ? await handles[n].catch(() => null) : null;
    if (hd) { roots.push(hd); await walkHandle(hd, hd, '', out); }
    else if (entries[n]) await walkEntry(entries[n], '', out);
    else if (files[n]) out.push({ file: files[n], rel: files[n].name });
  }
  if (roots.length) askWrite(roots);
  return out;
}

async function walkHandle(hd, root, prefix, out) {
  if (hd.kind === 'file') out.push({ file: await hd.getFile(), handle: hd, root, rel: prefix + hd.name });
  else for await (const child of hd.values()) await walkHandle(child, root, `${prefix}${hd.name}/`, out);
}

async function walkEntry(e, prefix, out) {
  if (e.isFile) {
    out.push({ file: await new Promise((res, rej) => e.file(res, rej)), rel: prefix + e.name });
  } else if (e.isDirectory) {
    const reader = e.createReader();
    for (;;) {
      const batch = await new Promise((res, rej) => reader.readEntries(res, rej));
      if (!batch.length) break;
      for (const c of batch) await walkEntry(c, `${prefix}${e.name}/`, out);
    }
  }
}

/* ---------- uploads ---------- */

const UPLOAD_PARALLEL = 3;
const queue = [];
let active = 0;

function enqueueUpload(item) {
  const tr = skeleton();
  tr.dataset.state = 'uploading';
  const [cName, cFmt, cOrig, , cGain, cStatus] = tr.children;
  cName.append(nameCell(item.rel, false));
  cFmt.textContent = '—';
  cOrig.textContent = bytes(item.file.size);
  cGain.append(gainCell(1, 1, false));
  cStatus.append(h('div', { class: 'status' }, h('span', {}, 'Waiting to upload')));
  pendingBody.append(tr);
  refreshEmpty();
  queue.push({ ...item, tr });
  pump();
}

function pump() {
  while (active < UPLOAD_PARALLEL && queue.length) {
    const item = queue.shift();
    active++;
    upload(item).finally(() => { active--; pump(); });
  }
}

function upload(item) {
  const status = $('.status span', item.tr);
  return new Promise(resolve => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', 'api/upload');
    xhr.upload.onprogress = ev => {
      if (!ev.lengthComputable) return;
      const p = (ev.loaded / ev.total) * 100;
      item.tr.style.setProperty('--p', `${p}%`);
      status.textContent = `Uploading ${p.toFixed(0)}%`;
    };
    const fail = msg => {
      item.tr.dataset.state = 'failed';
      status.textContent = `Upload failed: ${msg}`;
      $('.c-act', item.tr).replaceChildren(h('span', { class: 'row-act' },
        h('button', { type: 'button', title: 'Remove', onclick: () => { item.tr.remove(); refreshEmpty(); } }, icon(ICON.x))));
      resolve();
    };
    xhr.onload = () => {
      if (xhr.status !== 200) return fail(xhr.responseText.trim() || xhr.statusText);
      const { id } = JSON.parse(xhr.responseText);
      uploads.set(id, { file: item.file, handle: item.handle, root: item.root, rel: item.rel, wb: 'none' });
      item.tr.remove();
      const e = jobs.get(id);
      if (e) upsert(e);
      refreshEmpty();
      resolve();
    };
    xhr.onerror = () => fail('connection lost');
    const fd = new FormData();
    fd.append('file', item.file, item.file.name);
    xhr.send(fd);
  });
}

/* ---------- write back (File System Access) ---------- */

// askWrite asks for write access right after a drop, while the user is still paying attention.
// Without a user activation the browser refuses; the "Write back" button then serves as the fallback.
async function askWrite(roots) {
  for (const r of roots) {
    try { await r.requestPermission({ mode: 'readwrite' }); } catch { /* needs a user gesture */ }
  }
  // Files that finished while the prompt was open can be written now.
  for (const [id, u] of uploads) if (u.wb === 'needs') writeBack(id);
}

async function writeBack(id, gesture = false) {
  const u = uploads.get(id);
  const e = jobs.get(id);
  if (!u?.handle || !e || e.state !== 'done' || e.size >= e.original) return;
  // Every event for a done job calls this; without the busy flag two calls could both write,
  // and the second would then see its own write as "file changed since upload".
  if (u.busy || u.wb === 'written' || (u.wb === 'failed' && !gesture)) return;
  u.busy = true;
  try {
    await writeBackNow(id, u, gesture);
  } finally {
    u.busy = false;
  }
}

async function writeBackNow(id, u, gesture) {
  // The row may have been removed from the list meanwhile; rendering would bring it back.
  const show = () => { if (rows.has(id)) render(jobs.get(id)); updateWriteBackButton(); };
  let perm = await u.handle.queryPermission({ mode: 'readwrite' });
  if (perm !== 'granted' && gesture) {
    perm = await (u.root ?? u.handle).requestPermission({ mode: 'readwrite' }).catch(() => 'denied');
    if (perm === 'granted' && u.root !== u.handle) perm = await u.handle.queryPermission({ mode: 'readwrite' });
  }
  if (perm !== 'granted') {
    u.wb = 'needs';
    return show();
  }

  u.wb = 'writing';
  show();
  try {
    const cur = await u.handle.getFile();
    if (cur.size !== u.file.size || cur.lastModified !== u.file.lastModified) throw new Error('file changed since upload');
    const r = await fetch(`api/download/${id}`);
    if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
    const blob = await r.blob();
    const w = await u.handle.createWritable();
    await w.write(blob);
    await w.close();
    u.wb = 'written';
  } catch (err) {
    u.wb = 'failed';
    u.err = err.message;
  }
  show();
}

function updateWriteBackButton() {
  const n = [...uploads.values()].filter(u => u.wb === 'needs').length;
  $('#writeback').hidden = n === 0;
  $('#wb-count').textContent = n;
}

$('#writeback').addEventListener('click', async () => {
  for (const [id, u] of uploads) if (u.wb === 'needs') await writeBack(id, true);
});

/* ---------- settings ---------- */

const LEVELS = ['Fastest', 'Faster', 'Fast', 'Moderate', 'Balanced', 'Thorough', 'Intense', 'Maximum', 'Extreme'];
const LEVEL_HINT = ['Quick single passes', 'Quick single passes', 'Light', 'Light', 'Good balance of time and size',
  'More iterations', 'Many iterations — slow', 'Very slow', 'Exhaustive — can take minutes per file'];

const PDF_PROFILES = [
  ['none', 'Off — lossless only'],
  ['screen', 'Lossy: Screen — 72 dpi'],
  ['ebook', 'Lossy: eBook — 150 dpi'],
  ['printer', 'Lossy: Printer — 300 dpi'],
  ['prepress', 'Lossy: Prepress — 300 dpi, colour preserving'],
  ['custom', 'Lossy: Custom dpi'],
];

const SCHEMA = [
  { id: 'general', title: 'General', fields: [
    { type: 'level', key: 'level' },
    { type: 'number', key: 'workers', label: 'Parallel files', hint: '0 uses one file per CPU core', min: 0, max: 512 },
    { type: 'bool', key: 'preserveTimestamps', label: 'Preserve timestamps', hint: 'Keep the modification time of optimized files' },
    { type: 'bool', key: 'lowPriority', label: 'Low priority', hint: 'Run tools at below-normal priority so the machine stays responsive' },
    { type: 'bool', key: 'skipOptimized', label: 'Skip already optimized', hint: 'Remember results and skip files fox already optimized with the same settings' },
    { type: 'list', key: 'include', label: 'Only include', hint: 'Name patterns, comma separated, e.g. *.png, *.jpg — empty includes everything' },
    { type: 'list', key: 'exclude', label: 'Exclude', hint: 'Name patterns that are never touched, e.g. *.min.js' },
  ] },
  { id: 'images', title: 'Images', fields: [
    { type: 'note', text: 'Images are optimized losslessly by default: every pixel stays identical. Each “Allow lossy” switch is an opt-in for smaller files whose pixels change. Removing metadata never drops anything that changes how an image looks: EXIF orientation and non-sRGB colour profiles are always kept.' },
    { type: 'group', label: 'JPEG' },
    { type: 'bool', key: 'jpeg.lossy', label: 'Allow lossy', hint: 'Re-encode at visually lossless quality — pixels change' },
    { type: 'bool', key: 'jpeg.keepMetadata', label: 'Keep metadata', hint: 'EXIF, ICC profiles and comments' },
    { type: 'bool', key: 'jpeg.arithmetic', label: 'Arithmetic coding', hint: 'Smaller, but many viewers cannot open the result' },
    { type: 'group', label: 'PNG' },
    { type: 'bool', key: 'png.lossy', label: 'Allow lossy', hint: 'Quantize to a palette when quality stays high — pixels change' },
    { type: 'bool', key: 'png.keepMetadata', label: 'Keep metadata' },
    { type: 'group', label: 'GIF' },
    { type: 'bool', key: 'gif.lossy', label: 'Allow lossy', hint: 'Pixels change' },
    { type: 'bool', key: 'gif.keepMetadata', label: 'Keep metadata', hint: 'Comments, names and extensions' },
    { type: 'group', label: 'WebP' },
    { type: 'bool', key: 'webp.lossy', label: 'Allow lossy', hint: 'Pixels change' },
    { type: 'bool', key: 'webp.keepMetadata', label: 'Keep metadata', hint: 'EXIF, XMP and ICC profiles' },
    { type: 'group', label: 'TIFF & BMP' },
    { type: 'bool', key: 'tiff.keepMetadata', label: 'TIFF: keep metadata' },
    { type: 'bool', key: 'bmp.keepMetadata', label: 'BMP: keep metadata' },
  ] },
  { id: 'documents', title: 'Documents', fields: [
    { type: 'select', key: 'pdf.profile', label: 'PDF profile', hint: 'Every profile but Off is lossy: Ghostscript re-renders the whole PDF', options: PDF_PROFILES },
    { type: 'number', key: 'pdf.dpi', label: 'Image resolution', hint: 'Dots per inch for the custom profile', min: 36, max: 2400, when: s => s.pdf.profile === 'custom' },
    { type: 'bool', key: 'pdf.skipLayered', label: 'Skip layered PDFs', hint: 'Re-rendering can flatten optional content layers' },
  ] },
  { id: 'web', title: 'Web', fields: [
    { type: 'bool', key: 'web.html', label: 'HTML', hint: 'Minify .html — may alter whitespace-sensitive markup' },
    { type: 'bool', key: 'web.css', label: 'CSS' },
    { type: 'bool', key: 'web.js', label: 'JavaScript & JSON' },
    { type: 'list', key: 'web.jsExtensions', label: 'Extra JavaScript extensions', hint: 'Plain JavaScript only, e.g. .es — never JSX or TypeScript. Minified even when JavaScript is off; .mjs and .cjs need no entry' },
    { type: 'bool', key: 'web.svg', label: 'SVG', hint: 'Safe minification of vector images' },
  ] },
  { id: 'media', title: 'Audio & video', fields: [
    { type: 'bool', key: 'audio.keepMetadata', label: 'Keep audio metadata', hint: 'Tags, cover art and comments' },
    { type: 'bool', key: 'audio.trimSilence', label: 'Trim silence', hint: 'Remove leading and trailing silence from WAV and FLAC — changes the audio' },
    { type: 'bool', key: 'video.keepMetadata', label: 'Keep video metadata' },
  ] },
  { id: 'archives', title: 'Archives', fields: [
    { type: 'bool', key: 'archive.keepMetadata', label: 'Keep metadata', hint: 'Timestamps and comments inside archives' },
    { type: 'bool', key: 'archive.recurse', label: 'Optimize contents', hint: 'Also optimize images inside ZIP, Office documents and EPUB' },
  ] },
  { id: 'exe', title: 'Executables', fields: [
    { type: 'bool', key: 'exe.enabled', label: 'Optimize executables', hint: 'EXE and DLL files. Signed files and installers are always skipped' },
    { type: 'bool', key: 'exe.upx', label: 'Compress with UPX', hint: 'Smaller, but slower start and antivirus false positives', when: s => s.exe.enabled },
    { type: 'bool', key: 'exe.peTrim', label: 'Trim PE headers', when: s => s.exe.enabled },
  ] },
  { id: 'advanced', title: 'Advanced', fields: [
    { type: 'number', key: 'leanifyIterations', label: 'Leanify iterations', hint: '0 derives the count from the level', min: 0, max: 10000 },
    { type: 'number', key: 'pngwolfIterations', label: 'pngwolf iterations', hint: '0 derives the count from the level', min: 0, max: 10000 },
    { type: 'tools' },
  ] },
];

let settings = null;
let saveTimer;

const getKey = (o, key) => key.split('.').reduce((v, k) => v?.[k], o);
function setKey(o, key, val) {
  const ks = key.split('.');
  const last = ks.pop();
  ks.reduce((v, k) => (v[k] ??= {}), o)[last] = val;
}

function changed(key, val) {
  setKey(settings, key, val);
  for (const el of document.querySelectorAll('[data-when]')) el.hidden = !el._when(settings);
  const st = $('#save-state');
  st.className = 'save-state';
  st.textContent = 'Saving…';
  clearTimeout(saveTimer);
  saveTimer = setTimeout(save, 400);
}

async function save() {
  saveTimer = null;
  const st = $('#save-state');
  try {
    await api('api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(settings) });
    if (saveTimer) return;
    st.className = 'save-state saved';
    st.textContent = '✓ Saved';
  } catch (err) {
    st.className = 'save-state error';
    st.textContent = `Not saved: ${err.message}`;
  }
}

function field(f) {
  const val = f.key && getKey(settings, f.key);
  const txt = (extra = []) => h('span', { class: 'txt' }, h('span', { class: 'lbl' }, f.label), f.hint && h('span', { class: 'hint' }, f.hint), extra);
  switch (f.type) {
    case 'group':
      return h('h4', {}, f.label);
    case 'note':
      return h('p', { class: 'note' }, f.text);
    case 'bool': {
      const id = `f-${f.key}`;
      return h('label', { class: 'field', for: id }, txt(),
        h('input', { id, type: 'checkbox', class: 'switch', checked: !!val, onchange: ev => changed(f.key, ev.target.checked) }));
    }
    case 'number':
      return h('label', { class: 'field' }, txt(),
        h('input', { type: 'number', name: f.key, class: 'input num', min: f.min, max: f.max, value: val ?? 0,
          oninput: ev => { const n = parseInt(ev.target.value, 10); if (!Number.isNaN(n)) changed(f.key, Math.min(Math.max(n, f.min), f.max)); } }));
    case 'select':
      return h('label', { class: 'field' }, txt(),
        h('select', { name: f.key, class: 'select', onchange: ev => changed(f.key, ev.target.value) },
          f.options.map(([v, l]) => h('option', { value: v, selected: v === val }, l))));
    case 'list':
      return h('label', { class: 'field wide' }, txt(),
        h('input', { type: 'text', name: f.key, class: 'input text', spellcheck: false, value: (val ?? []).join(', '),
          oninput: ev => changed(f.key, ev.target.value.split(/[,\n]/).map(s => s.trim()).filter(Boolean)) }));
    case 'level':
      return levelField(f.key, val);
    case 'tools':
      return toolsField();
  }
}

function levelField(key, val) {
  const num = h('b', {}, String(val));
  const name = h('span', {}, LEVELS[val - 1]);
  const hint = h('small', {}, LEVEL_HINT[val - 1]);
  const range = h('input', { type: 'range', name: key, class: 'range', min: 1, max: 9, step: 1, value: val, 'aria-label': 'Optimization level' });
  const paint = v => {
    num.textContent = v;
    name.textContent = LEVELS[v - 1];
    hint.textContent = LEVEL_HINT[v - 1];
    range.style.setProperty('--fill', `${((v - 1) / 8) * 100}%`);
  };
  range.addEventListener('input', () => { const v = +range.value; paint(v); changed(key, v); });
  paint(val);
  return h('div', { class: 'level' },
    h('div', { class: 'level-top' }, num, name, hint),
    range,
    h('div', { class: 'ticks' }, Array.from({ length: 9 }, (_, i) => h('span', {}, String(i + 1)))),
    h('div', { class: 'ticks-names' }, h('span', {}, 'Fast'), h('span', {}, 'Extreme')));
}

function toolsField() {
  const grid = h('div', { class: 'tools' }, h('span', { class: 'hint' }, 'Loading tools…'));
  api('api/tools').then(list => {
    grid.replaceChildren(...list.map(t => h('label', { class: `tool${t.available ? '' : ' missing'}`, title: t.available ? t.name : `${t.name} was not found` },
      h('input', { type: 'checkbox', name: `tool-${t.name}`, checked: !(settings.disabledTools ?? []).includes(t.name),
        onchange: ev => {
          const set = new Set(settings.disabledTools ?? []);
          ev.target.checked ? set.delete(t.name) : set.add(t.name);
          changed('disabledTools', [...set].sort());
        } }),
      h('span', { class: 'tn' }, t.name),
      t.url && h('a', { href: t.url, target: '_blank', rel: 'noopener noreferrer', title: 'Project page', onclick: ev => ev.stopPropagation() }, '↗'))));
  }, err => grid.replaceChildren(h('span', { class: 'hint' }, `Could not load tools: ${err.message}`)));
  return h('div', { class: 'field wide' },
    h('span', { class: 'txt' }, h('span', { class: 'lbl' }, 'Tools'),
      h('span', { class: 'hint' }, 'Unchecked tools never run. Steps whose tool is unavailable are skipped automatically.')),
    grid,
    h('p', { class: 'note' }, 'To run a tool through wine or from a custom location, set ', h('code', {}, 'toolPaths'), ' in ', h('code', {}, '~/.fox/config.json'), '.'));
}

function buildSettings() {
  const form = $('#settings-form');
  const rail = $('#rail');
  form.replaceChildren();
  rail.replaceChildren();
  SCHEMA.forEach((sec, i) => {
    const n = String(i + 1).padStart(2, '0');
    const fields = sec.fields.map(f => {
      const el = field(f);
      if (f.when) { el.dataset.when = ''; el._when = f.when; el.hidden = !f.when(settings); }
      return el;
    });
    form.append(h('section', { class: 'sec', id: `s-${sec.id}` }, h('h3', {}, h('span', {}, n), sec.title), fields));
    rail.append(h('a', { href: `#s-${sec.id}`, 'data-sec': sec.id,
      onclick: ev => { ev.preventDefault(); $(`#s-${sec.id}`).scrollIntoView({ block: 'start' }); } }, h('span', {}, n), sec.title));
  });
  spy();
}

function spy() {
  const form = $('#settings-form');
  const links = [...$('#rail').children];
  const mark = () => {
    const top = form.getBoundingClientRect().top + 40;
    let cur = links[0];
    for (const a of links) if ($(`#s-${a.dataset.sec}`).getBoundingClientRect().top <= top) cur = a;
    if (form.scrollTop + form.clientHeight >= form.scrollHeight - 4) cur = links.at(-1);
    links.forEach(a => a.classList.toggle('on', a === cur));
  };
  form.onscroll = mark;
  mark();
}

async function openSettings() {
  try {
    settings = await api('api/settings');
  } catch (err) {
    return toast(err.message, true);
  }
  $('#save-state').textContent = '';
  buildSettings();
  const panel = $('#settings');
  $('#scrim').hidden = false;
  panel.inert = false;
  panel.classList.add('open');
  $('#close-settings').focus({ focusVisible: false });
}

function closeSettings() {
  const panel = $('#settings');
  panel.classList.remove('open');
  panel.inert = true;
  $('#scrim').hidden = true;
  if (saveTimer) { clearTimeout(saveTimer); save(); }
  $('#open-settings').focus();
}

$('#open-settings').addEventListener('click', openSettings);
$('#close-settings').addEventListener('click', closeSettings);
$('#scrim').addEventListener('click', closeSettings);
addEventListener('keydown', ev => { if (ev.key === 'Escape' && $('#settings').classList.contains('open')) closeSettings(); });

connect();
