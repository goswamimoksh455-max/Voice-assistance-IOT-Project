// Development STT evaluation dashboard. The application path replays each
// dataset recording through the production capture module (capture.js →
// worklet.js → VAP1 WebSocket frames), exactly like microphone audio.
import {loadWorklet, startCapture} from '/web/capture.js'

const $ = id => document.getElementById(id)
const ui = {
  conn: $('connection'), dataset: $('dataset'), language: $('language'), whisperLanguage: $('whisperLanguage'), count: $('count'),
  rate: $('rate'), mode: $('mode'), runAll: $('runAll'), runOne: $('runOne'), stop: $('stop'), save: $('save'), json: $('json'),
  csv: $('csv'), state: $('state'), summary: $('summary'), body: document.querySelector('#results tbody'), detail: $('detail'),
  detailText: $('detailText'), originalAudio: $('originalAudio'), whisperAudio: $('whisperAudio'),
}
const PASS_WER = 0.15, FAIL_WER = 0.4, MIN_CORRELATION = 0.98, MAX_FULL_PIPELINE = 3, TURN_TIMEOUT_MS = 120000
const params = new URLSearchParams(location.search)
let ws, ctx, turn = 0, fixtures = [], whisperModel = '', rows = [], selectedId = null, running = false, stopRequested = false
let connected
const ready = new Promise(resolve => { connected = resolve })
const waiters = new Map()

const status = text => { ui.state.textContent = text }
const escapeHTML = s => String(s ?? '').replace(/[&<>"]/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;'}[c]))
const pct = v => v === undefined || v === null || Number.isNaN(v) ? '—' : `${(v * 100).toFixed(1)}%`
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))

function connect() {
  ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws`)
  ws.binaryType = 'arraybuffer'
  ws.onopen = () => { ui.conn.textContent = 'Connected'; ui.conn.classList.add('online'); ws.send(JSON.stringify({type: 'debug_enable', version: 1, debug: true})); connected() }
  ws.onclose = () => { ui.conn.textContent = 'Disconnected'; ui.conn.classList.remove('online'); for (const w of waiters.values()) w.finish('WebSocket closed'); setTimeout(connect, 1500) }
  ws.onmessage = ({data}) => typeof data === 'string' ? onEvent(JSON.parse(data)) : onAudio(data)
}
const send = event => ws.send(JSON.stringify({version: 1, ...event}))

function onEvent(e) {
  // Errors raised before a turn id is known belong to the turn in progress.
  const w = waiters.get(e.turn_id) ?? (e.type === 'error' && !e.turn_id ? [...waiters.values()][0] : undefined)
  if (!w) return
  switch (e.type) {
    case 'stt_result': w.stt = e; break
    case 'llm_delta': w.answer += e.text; break
    case 'metrics': w.metrics = e.metrics; break
    case 'error': w.finish(`${e.code}: ${e.message}`); break
    case 'turn_complete': w.finish(); break
  }
}

function onAudio(buffer) {
  const view = new DataView(buffer), kind = view.getUint8(5), w = waiters.get(view.getUint32(8))
  if (!w) return
  if (kind === 4) w.whisperWav = buffer.slice(16)
  if (kind === 2) w.tts.push(buffer.slice(16))
}

async function audioContext() {
  const rate = Number(ui.rate.value) || undefined
  if (!ctx || ctx.state === 'closed' || (rate && ctx.sampleRate !== rate)) {
    await ctx?.close()
    ctx = new AudioContext(rate ? {sampleRate: rate} : {})
    await loadWorklet(ctx)
  }
  await ctx.resume()
  return ctx
}

// replay sends one recording through the application path and waits for the
// server's verdict. In STT-only mode the server stops after Whisper.
async function replay(fixture, language, full) {
  const c = await audioContext()
  const encoded = await (await fetch(`/dev/fixtures/${encodeURIComponent(fixture.id)}`)).arrayBuffer()
  const decoded = await c.decodeAudioData(encoded) // browser resamples 16 kHz to the context rate
  const source = c.createBufferSource()
  source.buffer = decoded
  const id = ++turn
  const w = {answer: '', tts: []}
  const finished = new Promise(resolve => { w.finish = error => { w.error ??= error; resolve() } })
  waiters.set(id, w)
  try {
    send({type: 'listen_start', turn_id: id, language, sample_rate: c.sampleRate, capture_sample_rate: c.sampleRate, stt_only: !full, fixture: fixture.id})
    const capture = startCapture({ctx: c, source, ws, turn: id})
    const ended = new Promise(resolve => { source.onended = resolve })
    source.start()
    await ended
    await capture.stop() // waits for the worklet's final block, like releasing the talk button
    w.frames = capture.frames
    const stoppedAt = performance.now()
    send({type: 'listen_stop', turn_id: id})
    await Promise.race([finished, sleep(TURN_TIMEOUT_MS).then(() => w.finish('timed out'))])
    w.clientMS = Math.round(performance.now() - stoppedAt)
    w.contextRate = c.sampleRate
    if (full && !w.error) w.ttsPlayable = await countPlayable(c, w.tts)
    return w
  } finally {
    waiters.delete(id)
  }
}

async function countPlayable(c, wavs) {
  let ok = 0
  for (const wav of wavs) { try { await c.decodeAudioData(wav.slice(0)); ok++ } catch {} }
  return ok
}

async function runFixture(fixture, full) {
  const language = ui.whisperLanguage.value || fixture.language
  const row = {id: fixture.id, language: fixture.language, source: fixture.source, reference: fixture.reference, language_sent: language, mode: full ? 'full' : 'stt_only'}
  upsert(row)
  const direct = await fetch('/dev/api/direct', {method: 'POST', body: JSON.stringify({fixture: fixture.id, language})}).then(r => r.json()).catch(e => ({error: e.message}))
  Object.assign(row, {direct_text: direct.text ?? '', direct_error: direct.error, direct_score: direct.score, direct_ms: direct.stt_ms})
  upsert(row)
  const app = await replay(fixture, language, full).catch(e => ({error: e.message, tts: []}))
  const ev = app.stt?.eval ?? {}
  Object.assign(row, {
    app_text: app.stt?.text ?? '', app_error: app.error ?? ev.error, app_score: ev.score, app_ms: ev.resample_ms !== undefined ? ev.resample_ms + ev.whisper_ms : undefined,
    app_client_ms: app.clientMS, frames: app.frames, context_rate: app.contextRate, input_rate: ev.input_sample_rate, whisper_rate: ev.whisper_sample_rate,
    original_ms: fixture.duration_ms, input_ms: ev.input_duration_ms, whisper_ms_duration: ev.whisper_duration_ms, fidelity: ev.fidelity, levels: ev.levels,
  })
  if (full) Object.assign(row, {answer: app.answer, llm_metrics: app.metrics, tts_frames: app.tts.length, tts_playable: app.ttsPlayable})
  if (app.whisperWav) row.whisperURL = URL.createObjectURL(new Blob([app.whisperWav], {type: 'audio/wav'}))
  row.status = verdict(row)
  upsert(row)
}

function verdict(r) {
  if (r.app_error || !r.app_score) return 'fail'
  if (r.app_score.script_mismatch || r.app_score.wer > FAIL_WER) return 'fail'
  if (r.mode === 'full' && !(r.tts_playable > 0)) return 'fail'
  if (r.app_score.wer > PASS_WER || (r.fidelity && r.fidelity.correlation < MIN_CORRELATION) || r.app_text !== r.direct_text) return 'review'
  return 'pass'
}

function selectedFixtures() {
  const n = Math.max(1, Number(ui.count.value) || 1)
  const perLanguage = {}
  return fixtures.filter(f => (!ui.dataset.value || f.source === ui.dataset.value) && (!ui.language.value || f.language === ui.language.value))
    .filter(f => (perLanguage[f.language] = (perLanguage[f.language] ?? 0) + 1) <= n)
}

async function runBatch(list) {
  const full = ui.mode.value === 'full'
  if (full) {
    list = list.slice(0, MAX_FULL_PIPELINE)
    if (!params.has('autorun') && !confirm(`Full pipeline calls Gemini once per recording (${list.length}). Continue?`)) return
  }
  running = true; stopRequested = false; setButtons()
  await ready
  for (const [i, fixture] of list.entries()) {
    if (stopRequested) { status(`Stopped after ${i} of ${list.length}`); break }
    status(`Running ${i + 1} of ${list.length}: ${fixture.id}`)
    await runFixture(fixture, full)
    renderSummary()
  }
  if (!stopRequested) status(`Finished ${list.length} recording(s) · Whisper model ${whisperModel} · AudioContext ${ctx?.sampleRate ?? '—'} Hz`)
  running = false; setButtons()
}

function upsert(row) {
  const i = rows.findIndex(r => r.id === row.id)
  if (i >= 0) rows[i] = row; else rows.push(row)
  renderTable()
}

function renderTable() {
  ui.body.innerHTML = rows.map(r => `<tr data-id="${escapeHTML(r.id)}" class="${r.id === selectedId ? 'selected' : ''}">
    <td>${escapeHTML(r.id)}</td><td>${r.language}</td><td>${r.source}</td><td class="text">${escapeHTML(r.reference)}</td>
    <td class="text">${escapeHTML(r.direct_error ? `⚠ ${r.direct_error}` : r.direct_text)}</td><td class="text">${escapeHTML(r.app_error ? `⚠ ${r.app_error}` : r.app_text)}</td>
    <td>${pct(r.direct_score?.wer)}</td><td>${pct(r.app_score?.wer)}</td>
    <td>${r.input_rate ? `${r.input_rate} → ${r.whisper_rate}` : '—'}</td>
    <td>${r.original_ms ? `${(r.original_ms / 1000).toFixed(2)} / ${r.whisper_ms_duration !== undefined ? (r.whisper_ms_duration / 1000).toFixed(2) : '—'} s` : '—'}</td>
    <td>${r.direct_ms ?? '—'} / ${r.app_ms ?? '—'} ms</td>
    <td>${r.fidelity ? `r=${r.fidelity.correlation.toFixed(4)} · ${r.fidelity.lag_ms.toFixed(1)} ms` : '—'}</td>
    <td class="status-${r.status ?? ''}">${r.status === 'review' ? 'needs review' : r.status ?? 'running'}</td></tr>`).join('')
}

function stats(values) {
  const v = values.filter(x => typeof x === 'number' && !Number.isNaN(x)).sort((a, b) => a - b)
  if (!v.length) return {n: 0}
  const q = p => { const pos = p * (v.length - 1), lo = Math.floor(pos), hi = Math.ceil(pos); return v[lo] + (v[hi] - v[lo]) * (pos - lo) }
  return {n: v.length, mean: v.reduce((a, b) => a + b, 0) / v.length, median: q(0.5), p95: q(0.95)}
}

function summarize() {
  const out = {}
  for (const language of [...new Set(rows.map(r => r.language))]) {
    const rs = rows.filter(r => r.language === language && r.status)
    const path = key => ({
      wer: stats(rs.map(r => r[`${key}_score`]?.wer)), cer: stats(rs.map(r => r[`${key}_score`]?.cer)), latency_ms: stats(rs.map(r => r[`${key}_ms`])),
      exact: rs.filter(r => r[`${key}_score`]?.exact).length, script_mismatch: rs.filter(r => r[`${key}_score`]?.script_mismatch).length,
      failures: rs.filter(r => r[`${key}_error`]).length,
      substitutions: rs.reduce((a, r) => a + (r[`${key}_score`]?.substitutions ?? 0), 0), deletions: rs.reduce((a, r) => a + (r[`${key}_score`]?.deletions ?? 0), 0),
      insertions: rs.reduce((a, r) => a + (r[`${key}_score`]?.insertions ?? 0), 0),
      technical: `${rs.reduce((a, r) => a + (r[`${key}_score`]?.technical_recognized ?? 0), 0)}/${rs.reduce((a, r) => a + (r[`${key}_score`]?.technical_words ?? 0), 0)}`,
    })
    out[language] = {n: rs.length, direct: path('direct'), application: path('app'), identical_transcripts: rs.filter(r => r.app_text === r.direct_text).length,
      fidelity_correlation: stats(rs.map(r => r.fidelity?.correlation)), status: Object.fromEntries(['pass', 'review', 'fail'].map(s => [s, rs.filter(r => r.status === s).length]))}
  }
  return out
}

function renderSummary() {
  const s = summarize(), fmt = st => st.n ? `mean ${pct(st.mean)} · median ${pct(st.median)}` : '—', ms = st => st.n ? `median ${Math.round(st.median)} · p95 ${Math.round(st.p95)} ms` : '—'
  ui.summary.classList.remove('muted')
  ui.summary.innerHTML = Object.entries(s).map(([language, x]) => `<div class="metric"><strong>${language} · ${x.n} recordings · pass ${x.status.pass} / review ${x.status.review} / fail ${x.status.fail}</strong>
    <small>Direct WER ${fmt(x.direct.wer)} · CER ${fmt(x.direct.cer)} · ${ms(x.direct.latency_ms)} · failures ${x.direct.failures}</small>
    <small>App WER ${fmt(x.application.wer)} · CER ${fmt(x.application.cer)} · ${ms(x.application.latency_ms)} · failures ${x.application.failures}</small>
    <small>App edits: ${x.application.substitutions} sub · ${x.application.deletions} del · ${x.application.insertions} ins · exact ${x.application.exact} · script mismatch ${x.application.script_mismatch}${language === 'hinglish' ? ` · technical words ${x.application.technical}` : ''}</small>
    <small>Identical direct/app transcripts ${x.identical_transcripts}/${x.n} · audio correlation ${x.fidelity_correlation.n ? `min ${Math.min(...rows.filter(r => r.language === language && r.fidelity).map(r => r.fidelity.correlation)).toFixed(4)}` : '—'}</small></div>`).join('')
}

function report() {
  return {created: new Date().toISOString(), whisper_model: whisperModel, audio_context_rate: ctx?.sampleRate, settings: {dataset: ui.dataset.value, language: ui.language.value, whisper_language: ui.whisperLanguage.value, count: ui.count.value, rate: ui.rate.value, mode: ui.mode.value},
    thresholds: {pass_wer: PASS_WER, fail_wer: FAIL_WER, min_correlation: MIN_CORRELATION}, user_agent: navigator.userAgent, summary: summarize(), rows: rows.map(({whisperURL, ...r}) => r)}
}

function csv() {
  const cols = ['id', 'language', 'source', 'language_sent', 'reference', 'direct_text', 'app_text', 'direct_wer', 'app_wer', 'direct_cer', 'app_cer', 'context_rate', 'input_rate', 'whisper_rate', 'original_ms', 'whisper_ms_duration', 'direct_ms', 'app_ms', 'correlation', 'lag_ms', 'status', 'direct_error', 'app_error']
  const value = (r, c) => ({direct_wer: r.direct_score?.wer, app_wer: r.app_score?.wer, direct_cer: r.direct_score?.cer, app_cer: r.app_score?.cer, correlation: r.fidelity?.correlation, lag_ms: r.fidelity?.lag_ms}[c] ?? r[c] ?? '')
  return [cols.join(','), ...rows.map(r => cols.map(c => `"${String(value(r, c)).replaceAll('"', '""')}"`).join(','))].join('\n')
}

function download(name, type, text) {
  const a = Object.assign(document.createElement('a'), {href: URL.createObjectURL(new Blob([text], {type})), download: name})
  a.click(); setTimeout(() => URL.revokeObjectURL(a.href), 1000)
}

async function saveReport(label = params.get('label') || 'dashboard') {
  const r = await fetch(`/dev/api/reports?label=${encodeURIComponent(label)}`, {method: 'POST', body: JSON.stringify(report())})
  const {path} = await r.json()
  status(`Report saved: ${path}`)
  return path
}

function setButtons() {
  ui.runAll.disabled = ui.runOne.disabled = running || !fixtures.length
  ui.stop.disabled = !running
}

function showDetail(id) {
  const r = rows.find(row => row.id === id)
  if (!r) return
  selectedId = id; renderTable(); ui.detail.hidden = false
  const {whisperURL, ...data} = r
  ui.detailText.textContent = JSON.stringify(data, null, 2)
  ui.originalAudio.src = `/dev/fixtures/${encodeURIComponent(id)}`
  if (whisperURL) ui.whisperAudio.src = whisperURL; else ui.whisperAudio.removeAttribute('src')
}

ui.body.onclick = event => { const tr = event.target.closest('tr'); if (tr) showDetail(tr.dataset.id) }
ui.runAll.onclick = () => runBatch(selectedFixtures())
ui.runOne.onclick = () => { const f = fixtures.find(x => x.id === selectedId) ?? selectedFixtures()[0]; if (f) runBatch([f]) }
ui.stop.onclick = () => { stopRequested = true; status('Stopping after the current recording…') }
ui.save.onclick = () => saveReport()
ui.json.onclick = () => download('stt-report.json', 'application/json', JSON.stringify(report(), null, 2))
ui.csv.onclick = () => download('stt-report.csv', 'text/csv', csv())

async function init() {
  connect()
  const data = await fetch('/dev/api/fixtures').then(r => r.json())
  fixtures = data.fixtures ?? []; whisperModel = data.whisper_model
  status(fixtures.length ? `${fixtures.length} cached recordings · Whisper model ${whisperModel}` : 'No fixtures cached. Run: go run ./cmd/fetch-fixtures')
  setButtons()
  if (params.has('autorun')) {
    for (const key of ['dataset', 'language', 'whisperLanguage', 'count', 'rate', 'mode']) if (params.has(key)) ui[key].value = params.get(key)
    await runBatch(selectedFixtures())
    window.__evalDone = {path: await saveReport(), summary: summarize()}
  }
}
init().catch(error => { status(`Error: ${error.message}`); window.__evalDone = {error: error.message} })
