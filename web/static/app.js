'use strict';

// ---------------------------------------------------------------- state

const S = {
  session: null,
  targets: [],
  samples: [],      // per target, sparse array indexed by seq
  summary: null,
  live: false,
  label: '',
  offset: 0,        // server clock minus browser clock
  connected: true,
  view: { kind: 'live', span: 60000 }, // or { kind: 'zoom', t0, t1 }
  selected: null,   // selected incident key
  hoverDetail: null,
  hoverOverview: null,
  drag: null,
};
let primary = 0;     // the target drawn as bars; others are lines
let seriesVar = [];  // CSS colour variable per target (null for primary)
let C = {};          // resolved colours

const $ = (s) => document.querySelector(s);
const el = (tag, cls, html) => {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (html != null) e.innerHTML = html;
  return e;
};

function now() {
  return S.live ? Date.now() + S.offset : S.summary.now;
}

// ---------------------------------------------------------------- data

async function load() {
  const r = await fetch('api/snapshot', { cache: 'no-store' });
  const snap = await r.json();
  const first = !S.session;
  S.session = snap.session;
  S.targets = snap.session.targets;
  S.live = snap.live;
  S.label = snap.label || '';
  S.samples = S.targets.map(() => []);
  for (const s of snap.samples) S.samples[s.tgt][s.seq] = s;
  setSummary(snap.summary);

  primary = S.targets.findIndex((t) => !t.gateway);
  if (primary < 0) primary = 0;
  let k = 0;
  seriesVar = S.targets.map((_, i) => (i === primary ? null : ['--s1', '--s2', '--s3'][Math.min(k++, 2)]));

  if (first) {
    if (!S.live) {
      const st = S.session.start;
      S.view = { kind: 'zoom', t0: st, t1: Math.min(st + 300000, Math.max(S.summary.now, st + 60000)) };
    }
    renderLegends();
  }
  renderPanels();
  drawAll();
}

function setSummary(sum) {
  S.summary = sum;
  if (S.live) S.offset = sum.now - Date.now();
}

function connect() {
  const es = new EventSource('api/events');
  let dropped = false;
  es.addEventListener('sample', (e) => {
    const s = JSON.parse(e.data);
    if (S.samples[s.tgt]) S.samples[s.tgt][s.seq] = s;
  });
  es.addEventListener('summary', (e) => {
    setSummary(JSON.parse(e.data));
    renderPanels();
    drawOverview();
  });
  es.onerror = () => {
    dropped = true;
    if (S.connected) { S.connected = false; renderHeader(); }
  };
  es.onopen = () => {
    S.connected = true;
    renderHeader();
    if (dropped) { dropped = false; load(); }
  };
}

// ---------------------------------------------------------------- formatting

const pad = (n) => String(n).padStart(2, '0');
function hms(t) { const d = new Date(t); return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`; }
function hm(t) { const d = new Date(t); return `${pad(d.getHours())}:${pad(d.getMinutes())}`; }
function fmtMs(v) {
  if (v == null) return '–';
  if (v < 10) return `${v.toFixed(1)} ms`;
  if (v < 1000) return `${Math.round(v)} ms`;
  return `${(v / 1000).toFixed(v < 10000 ? 2 : 1)} s`;
}
function fmtDur(s) {
  s = Math.round(s);
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} min ${pad(s % 60)} s`;
  return `${Math.floor(m / 60)} h ${pad(m % 60)} min`;
}
function fmtElapsed(s) {
  s = Math.max(0, Math.floor(s));
  return `${Math.floor(s / 3600)}:${pad(Math.floor(s / 60) % 60)}:${pad(s % 60)}`;
}
const pct = (v) => (v === 0 ? '0%' : v < 0.1 ? '<0.1%' : `${v.toFixed(1)}%`);
const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));

function classOf(rtt) {
  const th = S.session.thresholds;
  if (rtt < th.good_ms) return 'good';
  if (rtt < th.warn_ms) return 'fair';
  if (rtt < th.severe_ms) return 'high';
  return 'severe';
}

const ICON = {
  good: '<svg viewBox="0 0 12 12"><circle cx="6" cy="6" r="5.5" fill="var(--good)"/><path d="M3.4 6.2l1.8 1.8 3.4-3.6" stroke="#fff" stroke-width="1.6" fill="none" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  degraded: '<svg viewBox="0 0 12 12"><path d="M6 .8l5.4 10H.6z" fill="var(--fair)"/><path d="M6 4.4v3" stroke="#0b0b0b" stroke-width="1.4" stroke-linecap="round"/><circle cx="6" cy="9.1" r=".8" fill="#0b0b0b"/></svg>',
  bad: '<svg viewBox="0 0 12 12"><path d="M3.6.5h4.8l3.1 3.1v4.8l-3.1 3.1H3.6L.5 8.4V3.6z" fill="var(--severe)"/><path d="M4.2 4.2l3.6 3.6M7.8 4.2L4.2 7.8" stroke="#fff" stroke-width="1.5" stroke-linecap="round"/></svg>',
  waiting: '<svg viewBox="0 0 12 12"><circle cx="6" cy="6" r="4.8" stroke="var(--muted)" stroke-width="1.4" fill="none" stroke-dasharray="2.5 2"/></svg>',
};
const STATUS_LABEL = { good: 'Good', degraded: 'Degraded', bad: 'Bad', waiting: 'Waiting' };

// ---------------------------------------------------------------- panels

function renderHeader() {
  const mode = $('#mode');
  if (!S.live) {
    mode.className = 'mode replay';
    mode.innerHTML = `<span class="dot"></span>Replay${S.label ? ` · ${esc(S.label)}` : ''}`;
  } else if (!S.connected) {
    mode.className = 'mode offline';
    mode.innerHTML = '<span class="dot"></span>Disconnected (is pingmon still running?)';
  } else {
    mode.className = 'mode live';
    mode.innerHTML = '<span class="dot"></span>Live';
  }
  const start = new Date(S.session.start);
  $('#started').textContent = 'Started ' + start.toLocaleDateString([], { weekday: 'short', day: 'numeric', month: 'short' }) + ', ' + hm(start);
  $('#elapsed').textContent = (S.live ? 'running ' : 'lasted ') + fmtElapsed(S.summary.elapsed_s);
}

function renderPanels() {
  renderHeader();
  renderCards();
  renderSummary();
  renderIncidents();
}

function renderCards() {
  const box = $('#cards');
  box.replaceChildren(...S.summary.targets.map((t) => {
    const r = t.recent;
    const stalled = t.unanswered_for_s >= 3;
    const rtt = t.last_rtt_ms == null || stalled ? '–' : fmtMs(t.last_rtt_ms).replace(/ (ms|s)$/, '<small>$1</small>');
    let reason = t.reason ? t.reason[0].toUpperCase() + t.reason.slice(1) : '';
    if (t.status === 'good') reason = 'No problems in the last 30 s';
    const c = el('div', 'card');
    c.innerHTML = `
      <div class="head">
        <div><div class="name">${esc(t.name)}</div><div class="host">${esc(t.host)}</div></div>
        <span class="pill ${t.status}">${ICON[t.status] || ''}${STATUS_LABEL[t.status] || t.status}</span>
      </div>
      <div class="rtt">${rtt}</div>
      <div class="reason${t.status === 'bad' ? ' alert' : ''}">${esc(reason)}</div>
      <div class="recent">
        <span>last 30 s:</span>
        <span>median <b>${r.received ? fmtMs(r.p50_ms) : '–'}</b></span>
        <span>p95 <b>${r.received ? fmtMs(r.p95_ms) : '–'}</b></span>
        <span>lost <b>${r.lost + r.late}</b></span>
        <span>dup <b>${r.dups}</b></span>
        <span>jitter <b>${r.received > 1 ? fmtMs(r.jitter_ms) : '–'}</b></span>
      </div>`;
    return c;
  }));
}

function renderSummary() {
  const th = S.session.thresholds;
  const ts = S.summary.targets;
  const done = (s) => Math.max(1, s.sent - s.pending);
  const bar = (n, s, v) => `<span class="bar" style="width:${Math.max(n ? 2 : 0, Math.round(40 * n / done(s)))}px;background:var(${v})"></span>${pct(100 * n / done(s))}`;
  const rows = [
    ['group', 'Pings'],
    ['Sent', (s) => s.sent.toLocaleString()],
    ['Lost', (s) => `${s.lost} (${pct(s.loss_pct)})`],
    ['Late (after ' + S.session.loss_after_ms / 1000 + ' s)', (s) => `${s.late}`],
    ['Duplicate replies', (s) => `${s.dups}`],
    ['Longest run with no reply', (s) => (s.longest_outage_s ? fmtDur(s.longest_outage_s) : '–')],
    ['group', 'Latency'],
    ['Median', (s) => fmtMs(s.received ? s.p50_ms : null)],
    ['95th percentile', (s) => fmtMs(s.received ? s.p95_ms : null)],
    ['99th percentile', (s) => fmtMs(s.received ? s.p99_ms : null)],
    ['Worst', (s) => fmtMs(s.received ? s.max_ms : null)],
    ['Jitter', (s) => fmtMs(s.received > 1 ? s.jitter_ms : null)],
    ['group', 'Share of pings'],
    [`Good (&lt; ${th.good_ms} ms)`, (s) => bar(s.good, s, '--good')],
    [`Fair (&lt; ${th.warn_ms} ms)`, (s) => bar(s.fair, s, '--fair')],
    [`High (&lt; ${fmtMs(th.severe_ms)})`, (s) => bar(s.high, s, '--high')],
    [`Severe (≥ ${fmtMs(th.severe_ms)})`, (s) => bar(s.severe, s, '--severe')],
    ['Lost or late', (s) => bar(s.lost + s.late, s, '--severe')],
  ];
  const head = `<thead><tr><th></th>${ts.map((t) => `<th>${esc(t.name)}</th>`).join('')}</tr></thead>`;
  const body = rows.map(([label, f]) => label === 'group'
    ? `<tr class="group"><td colspan="${ts.length + 1}">${f}</td></tr>`
    : `<tr><td>${label}</td>${ts.map((t) => `<td>${f(t.total)}</td>`).join('')}</tr>`).join('');
  $('#summary').innerHTML = head + `<tbody>${body}</tbody>`;
}

const incidentKey = (i) => `${i.tgt}:${i.start_seq}`;

function renderIncidents() {
  const list = $('#incidents');
  const incs = S.summary.incidents;
  $('#incident-count').textContent = incs.length ? `(${incs.length})` : '';
  if (!incs.length) {
    list.innerHTML = '<li class="empty">None so far.</li>';
    return;
  }
  list.replaceChildren(...incs.map((i) => {
    const li = el('li');
    const key = incidentKey(i);
    if (key === S.selected) li.classList.add('selected');
    const colour = i.kind === 'high latency' ? 'var(--high)' : 'var(--severe)';
    const bits = [fmtDur(i.duration_s)];
    if (i.max_rtt_ms) bits.push('worst ' + fmtMs(i.max_rtt_ms));
    if (i.lost) bits.push(`${i.lost} lost`);
    if (i.late) bits.push(`${i.late} late`);
    if (i.dups) bits.push(`${i.dups} dup`);
    if (i.gateway_also === true) bits.push('router also affected');
    if (i.gateway_also === false) bits.push('router fine');
    const what = i.kind[0].toUpperCase() + i.kind.slice(1);
    li.innerHTML = `
      <svg class="icon" viewBox="0 0 12 12"><rect x="1" y="1" width="10" height="10" rx="2.5" fill="${colour}"/></svg>
      <span class="what">${what} · ${esc(S.targets[i.tgt].name)}${i.ongoing ? '<span class="tag">ongoing</span>' : ''}</span>
      <span class="when">${hms(i.start)}</span>
      <span class="detail">${bits.join(' · ')}</span>`;
    li.onclick = () => {
      S.selected = key;
      const padMs = Math.max(15000, (i.end - i.start) * 0.25);
      zoomTo(i.start - padMs, i.end + padMs);
      renderIncidents();
    };
    return li;
  }));
}

function renderLegends() {
  const th = S.session.thresholds;
  const series = S.targets
    .map((t, i) => (i === primary ? '' : `<span><i class="sw line" style="background:var(${seriesVar[i]})"></i>${esc(t.name)} (line)</span>`))
    .join('');
  const common = `
    <span><i class="sw" style="background:var(--good)"></i>&lt; ${th.good_ms} ms</span>
    <span><i class="sw" style="background:var(--fair)"></i>&lt; ${th.warn_ms} ms</span>
    <span><i class="sw" style="background:var(--high)"></i>&lt; ${fmtMs(th.severe_ms)}</span>
    <span><i class="sw" style="background:var(--severe)"></i>≥ ${fmtMs(th.severe_ms)}</span>`;
  $('#legend').innerHTML = `<span><b>${esc(S.targets[primary].name)}</b> (bars):</span>${common}
    <span><i class="sw late"></i>late reply</span>
    <span><i class="sw lost"></i>lost</span>
    <span><i class="sw" style="background:var(--pending)"></i>waiting</span>
    ${series}
    <span><i class="sw dot" style="background:var(--dup)"></i>duplicate reply</span>`;
  $('#legend-overview').innerHTML = `<span>Bars: <b>${esc(S.targets[primary].name)}</b> median (solid) and worst (faint) per block, coloured by latency:</span>${common}
    ${series.replace(/\(line\)/g, '(median)')}
    <span>Top rows: share of pings lost per target, and duplicates</span>`;
}

// ---------------------------------------------------------------- view

function detailRange() {
  if (S.view.kind === 'live') {
    const t1 = now();
    return [t1 - S.view.span, t1];
  }
  return [S.view.t0, S.view.t1];
}

function zoomTo(t0, t1) {
  const minSpan = 10000;
  if (t1 - t0 < minSpan) { const c = (t0 + t1) / 2; t0 = c - minSpan / 2; t1 = c + minSpan / 2; }
  S.view = { kind: 'zoom', t0, t1 };
  updateControls();
  drawAll();
}

function goLive() {
  S.view = { kind: 'live', span: S.view.kind === 'live' ? S.view.span : 60000 };
  S.selected = null;
  renderIncidents();
  updateControls();
  drawAll();
}

function updateControls() {
  const [t0, t1] = detailRange();
  const span = Math.round((t1 - t0) / 1000);
  for (const b of document.querySelectorAll('.seg button')) {
    b.setAttribute('aria-pressed', String(+b.dataset.span === span));
  }
  $('#back-live').hidden = !(S.live && S.view.kind === 'zoom');
  const title = $('#detail-title');
  if (S.view.kind === 'live') {
    title.textContent = span < 120 ? `Last ${span} seconds` : `Last ${Math.round(span / 60)} minutes`;
  } else {
    title.textContent = `${hms(t0)} – ${hms(t1)}`;
  }
}

// ---------------------------------------------------------------- drawing

function readColours() {
  const cs = getComputedStyle(document.documentElement);
  const g = (n) => cs.getPropertyValue(n).trim();
  C = {
    surface: g('--surface'), ink: g('--ink'), ink2: g('--ink-2'), muted: g('--muted'),
    grid: g('--grid'), axis: g('--axis'), pending: g('--pending'), dup: g('--dup'),
    good: g('--good'), fair: g('--fair'), high: g('--high'), severe: g('--severe'),
  };
  for (const v of ['--s1', '--s2', '--s3']) C[v] = g(v);
}

function prep(cv) {
  const dpr = window.devicePixelRatio || 1;
  const H = +cv.dataset.height;
  cv.style.height = H + 'px';
  const W = cv.clientWidth;
  if (cv.width !== Math.round(W * dpr) || cv.height !== Math.round(H * dpr)) {
    cv.width = Math.round(W * dpr);
    cv.height = Math.round(H * dpr);
  }
  const ctx = cv.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, W, H);
  return { ctx, W, H };
}

function layout(W, H) {
  const rowH = 14;
  const rows = S.targets.length + 1; // a loss row per target, then duplicates
  const rowTop = 2;
  const top = rowTop + rows * rowH + 16;
  const left = W < 500 ? 56 : 64, right = 10, bottom = 22;
  return { W, H, left, right, top, bottom: H - bottom, pw: W - left - right, ph: H - bottom - top, rowTop, rowH, rows };
}

const LOG_MIN = 0, LOG_MAX = 4; // 1 ms .. 10 s
function yOf(L, v) {
  const l = Math.log10(Math.min(Math.max(v, 1), 10000));
  return L.bottom - ((l - LOG_MIN) / (LOG_MAX - LOG_MIN)) * L.ph;
}
const rowY = (L, r) => L.rowTop + r * L.rowH + L.rowH / 2;

function drawFrame(ctx, L, t0, t1, xs) {
  ctx.font = '11px system-ui, -apple-system, "Segoe UI", sans-serif';
  ctx.textBaseline = 'middle';

  // Row labels
  ctx.fillStyle = C.muted;
  ctx.textAlign = 'right';
  S.targets.forEach((t, i) => ctx.fillText(t.name.length > 9 ? t.name.slice(0, 8) + '…' : t.name, L.left - 8, rowY(L, i)));
  ctx.fillText('dup', L.left - 8, rowY(L, S.targets.length));
  ctx.strokeStyle = C.grid;
  ctx.lineWidth = 1;
  for (let r = 0; r <= L.rows; r++) {
    const y = Math.round(L.rowTop + r * L.rowH) + 0.5;
    if (r === 0) continue;
    ctx.beginPath(); ctx.moveTo(L.left, y); ctx.lineTo(L.left + L.pw, y); ctx.stroke();
  }

  // Y grid (log scale)
  const labels = ['1 ms', '10 ms', '100 ms', '1 s', '10 s'];
  for (let i = 0; i <= 4; i++) {
    const y = Math.round(yOf(L, 10 ** i)) + 0.5;
    ctx.strokeStyle = i === 0 ? C.axis : C.grid;
    ctx.beginPath(); ctx.moveTo(L.left, y); ctx.lineTo(L.left + L.pw, y); ctx.stroke();
    ctx.fillStyle = C.muted;
    ctx.fillText(labels[i], L.left - 8, y);
  }
  // Threshold lines
  const th = S.session.thresholds;
  ctx.setLineDash([3, 3]);
  for (const [v, c] of [[th.warn_ms, C.high], [th.severe_ms, C.severe]]) {
    const y = Math.round(yOf(L, v)) + 0.5;
    ctx.strokeStyle = c;
    ctx.globalAlpha = 0.55;
    ctx.beginPath(); ctx.moveTo(L.left, y); ctx.lineTo(L.left + L.pw, y); ctx.stroke();
  }
  ctx.globalAlpha = 1;
  ctx.setLineDash([]);

  // X ticks
  const steps = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200].map((s) => s * 1000);
  const maxTicks = Math.max(2, Math.floor(L.pw / 80));
  const step = steps.find((s) => (t1 - t0) / s <= maxTicks) || steps[steps.length - 1];
  const tzOff = new Date(t0).getTimezoneOffset() * 60000;
  ctx.textAlign = 'center';
  ctx.textBaseline = 'top';
  ctx.fillStyle = C.muted;
  for (let t = Math.ceil((t0 - tzOff) / step) * step + tzOff; t <= t1; t += step) {
    const x = xs(t);
    if (x < L.left + 16 || x > L.left + L.pw - 16) continue;
    ctx.fillText(step < 60000 ? hms(t) : hm(t), x, L.bottom + 6);
    ctx.strokeStyle = C.axis;
    ctx.beginPath(); ctx.moveTo(Math.round(x) + 0.5, L.bottom); ctx.lineTo(Math.round(x) + 0.5, L.bottom + 4); ctx.stroke();
  }
}

function bar(ctx, x, y, w, h, r) {
  if (h <= 0 || w <= 0) return;
  r = Math.min(r, w / 2, h);
  ctx.beginPath();
  if (ctx.roundRect && r > 0.5) ctx.roundRect(x, y, w, h, [r, r, 0, 0]);
  else ctx.rect(x, y, w, h);
  ctx.fill();
}

let hatch = null;
function hatchPattern(ctx) {
  if (hatch && hatch.key === C.severe + C.surface) return hatch.p;
  const c = document.createElement('canvas');
  c.width = c.height = 6;
  const g = c.getContext('2d');
  g.fillStyle = C.severe; g.fillRect(0, 0, 6, 6);
  g.strokeStyle = C.surface; g.lineWidth = 1.6;
  g.beginPath(); g.moveTo(-1, 7); g.lineTo(7, -1); g.moveTo(-1, 1); g.lineTo(1, -1); g.moveTo(5, 7); g.lineTo(7, 5); g.stroke();
  hatch = { key: C.severe + C.surface, p: ctx.createPattern(c, 'repeat') };
  return hatch.p;
}

function cross(ctx, x, y, s, colour) {
  ctx.strokeStyle = colour;
  ctx.lineWidth = 1.6;
  ctx.beginPath(); ctx.moveTo(x - s, y - s); ctx.lineTo(x + s, y + s); ctx.moveTo(x + s, y - s); ctx.lineTo(x - s, y + s); ctx.stroke();
}

function seqRange(t0, t1) {
  const st = S.session.start, iv = S.session.interval_ms;
  return [Math.max(0, Math.floor((t0 - st) / iv) - 8), Math.ceil((t1 - st) / iv) + 8];
}

function drawDetail() {
  const cv = $('#detail');
  const { ctx, W, H } = prep(cv);
  const [t0, t1] = detailRange();
  const L = layout(W, H);
  const xs = (t) => L.left + ((t - t0) / (t1 - t0)) * L.pw;
  drawFrame(ctx, L, t0, t1, xs);

  const iv = S.session.interval_ms;
  const pxPer = (iv / (t1 - t0)) * L.pw;
  const bw = pxPer >= 4 ? Math.min(pxPer - 2, 12) : Math.max(1, pxPer - 0.5);
  const [a, b] = seqRange(t0, t1);
  const tNow = now();

  ctx.save();
  ctx.beginPath(); ctx.rect(L.left, 0, L.pw, L.H); ctx.clip();

  // Primary target: bars
  const P = S.samples[primary];
  for (let q = a; q <= b; q++) {
    const s = P[q];
    if (!s) continue;
    const x = xs(s.t) - bw / 2;
    if (s.st === 'ok' || s.st === 'late') {
      const y = yOf(L, s.rtt);
      ctx.fillStyle = s.st === 'late' ? hatchPattern(ctx) : C[classOf(s.rtt)];
      bar(ctx, x, y, bw, L.bottom - y, 3);
    } else if (s.st === 'lost') {
      ctx.fillStyle = C.severe;
      ctx.globalAlpha = 0.16;
      ctx.fillRect(x, L.top, bw, L.ph);
      ctx.globalAlpha = 1;
    } else if (s.st === 'pending') {
      const waited = tNow - s.t;
      if (waited > 200) {
        const y = yOf(L, waited);
        ctx.fillStyle = C.pending;
        bar(ctx, x, y, bw, L.bottom - y, 3);
      }
    }
  }

  // Secondary targets: lines
  S.targets.forEach((_, ti) => {
    if (ti === primary) return;
    const arr = S.samples[ti];
    const colour = C[seriesVar[ti]];
    ctx.strokeStyle = colour;
    ctx.lineWidth = 2;
    ctx.lineJoin = 'round';
    ctx.beginPath();
    let pen = false;
    const pts = [];
    for (let q = a; q <= b; q++) {
      const s = arr[q];
      if (!s || !(s.st === 'ok' || s.st === 'late')) { pen = false; continue; }
      const x = xs(s.t), y = yOf(L, s.rtt);
      if (pen) ctx.lineTo(x, y); else ctx.moveTo(x, y);
      pen = true;
      pts.push([x, y]);
    }
    ctx.stroke();
    if (pxPer >= 6) {
      ctx.fillStyle = colour;
      ctx.strokeStyle = C.surface;
      ctx.lineWidth = 1.5;
      for (const [x, y] of pts) { ctx.beginPath(); ctx.arc(x, y, 3, 0, 7); ctx.fill(); ctx.stroke(); }
    }
  });

  // Event rows: losses per target, then duplicates
  const mark = Math.max(2.5, Math.min(4, bw / 2));
  S.targets.forEach((_, ti) => {
    const arr = S.samples[ti];
    for (let q = a; q <= b; q++) {
      const s = arr[q];
      if (!s) continue;
      const x = xs(s.t), y = rowY(L, ti);
      if (s.st === 'lost') cross(ctx, x, y, mark, C.severe);
      else if (s.st === 'late') {
        ctx.strokeStyle = C.severe; ctx.lineWidth = 1.4;
        ctx.beginPath(); ctx.arc(x, y, mark, 0, 7); ctx.stroke();
      }
      if (s.dup) {
        ctx.fillStyle = C.dup;
        ctx.beginPath(); ctx.arc(x, rowY(L, S.targets.length), Math.max(mark, 3.5), 0, 7); ctx.fill();
      }
    }
  });
  ctx.restore();

  // Hover crosshair + tooltip
  const tip = $('#detail-tip');
  if (S.hoverDetail == null || S.hoverDetail < L.left || S.hoverDetail > L.left + L.pw) {
    tip.hidden = true;
    return;
  }
  const t = t0 + ((S.hoverDetail - L.left) / L.pw) * (t1 - t0);
  const q = Math.round((t - S.session.start) / iv);
  const ref = S.samples[primary][q] || S.samples.map((arr) => arr[q]).find(Boolean);
  if (!ref) { tip.hidden = true; return; }
  const x = Math.round(xs(ref.t)) + 0.5;
  ctx.strokeStyle = C.ink2; ctx.globalAlpha = 0.5; ctx.lineWidth = 1;
  ctx.beginPath(); ctx.moveTo(x, L.rowTop); ctx.lineTo(x, L.bottom); ctx.stroke();
  ctx.globalAlpha = 1;
  const rows = S.targets.map((tg, ti) => {
    const s = S.samples[ti][q];
    const sw = ti === primary
      ? `<i class="sw" style="background:${s && (s.st === 'ok' || s.st === 'late') ? C[classOf(s.rtt)] : C.pending}"></i>`
      : `<i class="sw line" style="background:${C[seriesVar[ti]]}"></i>`;
    let v = '–';
    if (s) {
      v = { ok: fmtMs(s.rtt), late: `${fmtMs(s.rtt)} (late)`, lost: 'lost', pending: 'waiting…' }[s.st];
      if (s.dup) v += ` +${s.dup} dup`;
    }
    return `<div class="row"><span class="k">${sw}${esc(tg.name)}</span><b>${v}</b></div>`;
  });
  tip.innerHTML = `<div class="t">${hms(ref.t)} · #${q}</div>${rows.join('')}`;
  placeTip(tip, x, L.top, W);
}

function placeTip(tip, x, y, W) {
  tip.hidden = false;
  const w = tip.offsetWidth;
  const left = x + 12 + w > W ? x - 12 - w : x + 12;
  tip.style.left = Math.max(0, left) + 'px';
  tip.style.top = y + 'px';
}

// Aggregate one target's samples into fixed-width time blocks.
function buckets(ti, t0, bs, n) {
  const out = Array.from({ length: n }, () => ({ rtts: [], lost: 0, late: 0, dups: 0, n: 0 }));
  for (const s of S.samples[ti]) {
    if (!s || s.st === 'pending') continue;
    const i = Math.floor((s.t - t0) / bs);
    if (i < 0 || i >= n) continue;
    const b = out[i];
    b.n++;
    b.dups += s.dup || 0;
    if (s.st === 'lost') b.lost++;
    else {
      if (s.st === 'late') b.late++;
      b.rtts.push(s.rtt);
    }
  }
  for (const b of out) {
    if (!b.rtts.length) continue;
    b.rtts.sort((x, y) => x - y);
    b.med = b.rtts[Math.floor((b.rtts.length - 1) / 2)];
    b.max = b.rtts[b.rtts.length - 1];
  }
  return out;
}

let overviewCache = null;

function overviewRange() {
  const st = S.session.start;
  return [st, Math.max(now(), st + 10 * 60000)];
}

function drawOverview() {
  const cv = $('#overview');
  const { ctx, W, H } = prep(cv);
  const [t0, t1] = overviewRange();
  const L = layout(W, H);
  const xs = (t) => L.left + ((t - t0) / (t1 - t0)) * L.pw;
  drawFrame(ctx, L, t0, t1, xs);

  const sizes = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600].map((s) => s * 1000);
  const bs = sizes.find((s) => (t1 - t0) / s <= L.pw / 4) || sizes[sizes.length - 1];
  const n = Math.ceil((t1 - t0) / bs);
  const all = S.targets.map((_, ti) => buckets(ti, t0, bs, n));
  overviewCache = { t0, t1, bs, all, L };
  const bpx = (bs / (t1 - t0)) * L.pw;
  const gap = bpx >= 6 ? 2 : bpx >= 3 ? 1 : 0;
  const bw = Math.max(1, bpx - gap);

  ctx.save();
  ctx.beginPath(); ctx.rect(L.left, 0, L.pw, L.H); ctx.clip();

  all[primary].forEach((b, i) => {
    const x = xs(t0 + i * bs) + gap / 2;
    if (b.n && b.lost === b.n) {
      ctx.fillStyle = C.severe; ctx.globalAlpha = 0.16;
      ctx.fillRect(x, L.top, bw, L.ph);
      ctx.globalAlpha = 1;
    }
    if (!b.rtts.length) return;
    const yMax = yOf(L, b.max), yMed = yOf(L, b.med);
    ctx.fillStyle = C[classOf(b.max)];
    ctx.globalAlpha = 0.35;
    bar(ctx, x, yMax, bw, L.bottom - yMax, 2);
    ctx.globalAlpha = 1;
    ctx.fillStyle = C[classOf(b.med)];
    bar(ctx, x, yMed, bw, L.bottom - yMed, 2);
  });

  S.targets.forEach((_, ti) => {
    if (ti === primary) return;
    ctx.strokeStyle = C[seriesVar[ti]];
    ctx.lineWidth = 1.5;
    ctx.lineJoin = 'round';
    ctx.beginPath();
    let pen = false;
    all[ti].forEach((b, i) => {
      if (!b.rtts.length) { pen = false; return; }
      const x = xs(t0 + (i + 0.5) * bs), y = yOf(L, b.med);
      if (pen) ctx.lineTo(x, y); else ctx.moveTo(x, y);
      pen = true;
    });
    ctx.stroke();
  });

  // Event rows: share of pings lost or late per block, and duplicates
  const rh = L.rowH - 4;
  all.forEach((bk, ti) => {
    bk.forEach((b, i) => {
      const x = xs(t0 + i * bs) + gap / 2;
      const bad = b.lost + b.late;
      if (bad) {
        ctx.fillStyle = C.severe;
        ctx.globalAlpha = 0.35 + 0.65 * Math.min(1, bad / b.n);
        ctx.fillRect(x, rowY(L, ti) - rh / 2, Math.max(bw, 2), rh);
      }
      if (b.dups && ti === primary) {
        ctx.fillStyle = C.dup;
        ctx.globalAlpha = 0.45 + 0.55 * Math.min(1, b.dups / Math.max(1, b.n));
        ctx.fillRect(x, rowY(L, S.targets.length) - rh / 2, Math.max(bw, 2), rh);
      }
      ctx.globalAlpha = 1;
    });
  });
  // Duplicates on other targets share the row, drawn only where the primary has none.
  all.forEach((bk, ti) => {
    if (ti === primary) return;
    bk.forEach((b, i) => {
      if (!b.dups || all[primary][i].dups) return;
      ctx.fillStyle = C.dup;
      ctx.globalAlpha = 0.45;
      ctx.fillRect(xs(t0 + i * bs) + gap / 2, rowY(L, S.targets.length) - rh / 2, Math.max(bw, 2), rh);
      ctx.globalAlpha = 1;
    });
  });

  // Current detail window
  const [d0, d1] = S.drag ? [Math.min(S.drag.a, S.drag.b), Math.max(S.drag.a, S.drag.b)] : detailRange();
  const x0 = Math.max(L.left, xs(d0)), x1 = Math.min(L.left + L.pw, xs(d1));
  ctx.fillStyle = C.ink;
  ctx.globalAlpha = 0.07;
  ctx.fillRect(x0, L.rowTop, Math.max(2, x1 - x0), L.bottom - L.rowTop);
  ctx.globalAlpha = 0.5;
  ctx.strokeStyle = C.ink2;
  ctx.lineWidth = 1;
  ctx.strokeRect(Math.round(x0) + 0.5, L.rowTop + 0.5, Math.max(2, Math.round(x1 - x0) - 1), L.bottom - L.rowTop - 1);
  ctx.globalAlpha = 1;
  ctx.restore();

  drawOverviewTip();
}

function drawOverviewTip() {
  const tip = $('#overview-tip');
  const oc = overviewCache;
  const hx = S.hoverOverview;
  if (!oc || hx == null || S.drag || hx < oc.L.left || hx > oc.L.left + oc.L.pw) { tip.hidden = true; return; }
  const t = oc.t0 + ((hx - oc.L.left) / oc.L.pw) * (oc.t1 - oc.t0);
  const i = Math.floor((t - oc.t0) / oc.bs);
  const bt = oc.t0 + i * oc.bs;
  if (bt > now()) { tip.hidden = true; return; }
  const rows = S.targets.map((tg, ti) => {
    const b = oc.all[ti][i];
    if (!b || !b.n) return `<div class="row"><span class="k">${esc(tg.name)}</span><b>–</b></div>`;
    const parts = [];
    if (b.rtts.length) parts.push(`median ${fmtMs(b.med)}, worst ${fmtMs(b.max)}`);
    if (b.lost) parts.push(`${b.lost} lost`);
    if (b.late) parts.push(`${b.late} late`);
    if (b.dups) parts.push(`${b.dups} dup`);
    return `<div class="row"><span class="k">${esc(tg.name)}</span><b>${parts.join(', ')}</b></div>`;
  });
  tip.innerHTML = `<div class="t">${hms(bt)} – ${hms(bt + oc.bs)}</div>${rows.join('')}`;
  placeTip(tip, hx, oc.L.top, oc.L.W);
}

function drawAll() {
  if (!S.session) return;
  drawDetail();
  drawOverview();
}

// ---------------------------------------------------------------- interaction

function wire() {
  for (const b of document.querySelectorAll('.seg button')) {
    b.onclick = () => {
      const span = +b.dataset.span * 1000;
      if (S.view.kind === 'live') {
        S.view.span = span;
      } else {
        const c = (S.view.t0 + S.view.t1) / 2;
        S.view = { kind: 'zoom', t0: c - span / 2, t1: c + span / 2 };
      }
      updateControls();
      drawAll();
    };
  }
  $('#back-live').onclick = goLive;

  const d = $('#detail');
  d.addEventListener('pointermove', (e) => { S.hoverDetail = e.offsetX; drawDetail(); });
  d.addEventListener('pointerleave', () => { S.hoverDetail = null; drawDetail(); });

  const o = $('#overview');
  const tAt = (x) => {
    const oc = overviewCache;
    return oc.t0 + ((x - oc.L.left) / oc.L.pw) * (oc.t1 - oc.t0);
  };
  o.addEventListener('pointerdown', (e) => {
    if (!overviewCache) return;
    o.setPointerCapture(e.pointerId);
    S.drag = { x: e.offsetX, a: tAt(e.offsetX), b: tAt(e.offsetX) };
  });
  o.addEventListener('pointermove', (e) => {
    S.hoverOverview = e.offsetX;
    if (S.drag) S.drag.b = tAt(e.offsetX);
    drawOverview();
  });
  o.addEventListener('pointerup', (e) => {
    if (!S.drag) return;
    const drag = S.drag;
    S.drag = null;
    S.selected = null;
    renderIncidents();
    if (Math.abs(e.offsetX - drag.x) < 5) {
      const [d0, d1] = detailRange();
      const span = Math.max(d1 - d0, 120000);
      zoomTo(drag.a - span / 2, drag.a + span / 2);
    } else {
      zoomTo(Math.min(drag.a, drag.b), Math.max(drag.a, drag.b));
    }
  });
  o.addEventListener('pointerleave', () => { S.hoverOverview = null; drawOverview(); });

  window.addEventListener('resize', drawAll);
  const mq = matchMedia('(prefers-color-scheme: dark)');
  mq.addEventListener('change', () => { readColours(); drawAll(); });
}

async function main() {
  readColours();
  wire();
  await load();
  updateControls();
  if (S.live) {
    connect();
    setInterval(() => { if (S.view.kind === 'live') drawDetail(); }, 200);
  }
}

main().catch((e) => {
  document.querySelector('main').prepend(el('p', 'panel', 'Could not load data: ' + esc(e.message)));
});
