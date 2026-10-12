import {loadWorklet, startCapture} from './capture.js'

const $ = id => document.getElementById(id)
const ui = {
  conn: $('connection'), talk: $('talk'), stop: $('stop'), clear: $('clear'), state: $('state'),
  lang: $('language'), microphone: $('microphone'), user: $('userText'), assistant: $('assistantText'),
  form: $('textForm'), input: $('textInput'), metrics: $('metrics'), debugPanel: $('debugPanel'),
  debugToggle: $('debugToggle'), debugStats: $('debugStats'), levelBar: $('levelBar'),
  debugInputAudio: $('debugInputAudio'), debugWhisperAudio: $('debugWhisperAudio'),
  debugDownload: $('debugDownload'), expectedText: $('expectedText'), rawTranscript: $('rawTranscript'),
  compareTranscript: $('compareTranscript'), comparison: $('comparison')
}

let ws, stream, source, capture, ctx
let turn = 0, playing = [], audioQueue = Promise.resolve()
let speaking = false, recording = false, starting = false, stopping = false, pressHeld = false
let turnStoppedAt = 0, firstAudible = null, lastMetrics = null, cancelledTurn = 0
let debugURLs = [], debugInfo = null

function status(text) { ui.state.textContent = text }
function sendEvent(type, extra = {}) {
  if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({type, version: 1, ...extra}))
}
function connect() {
  ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws`)
  ws.binaryType = 'arraybuffer'
  ws.onopen = () => { ui.conn.textContent = 'Connected'; ui.conn.classList.add('online') }
  ws.onclose = () => { ui.conn.textContent = 'Disconnected'; ui.conn.classList.remove('online'); ui.talk.disabled = true; setTimeout(connect, 1500) }
  ws.onerror = () => status('Connection error')
  ws.onmessage = message => typeof message.data === 'string' ? control(JSON.parse(message.data)) : receiveAudio(message.data)
}

function control(message) {
  if (message.turn_id && message.turn_id !== turn) return
  switch (message.type) {
    case 'hello':
      ui.debugPanel.hidden = !message.debug
      break
    case 'ready':
      // listen_start is acknowledged with 'ready'; keep showing "Listening" while recording.
      ui.talk.disabled = false; if (!recording) status(message.message || 'Ready')
      break
    case 'debug_state':
      ui.debugToggle.checked = message.debug
      break
    case 'audio_debug':
      debugInfo = message.audio
      renderDebugStats()
      break
    case 'audio_debug_transcript':
      ui.rawTranscript.textContent = message.text
      compareTranscript()
      break
    case 'stt_result':
      ui.user.textContent = message.text; ui.user.classList.remove('muted'); ui.assistant.textContent = ''; status('Thinking…')
      break
    case 'llm_delta':
      ui.assistant.classList.remove('muted'); ui.assistant.textContent += message.text; status('Replying…')
      break
    case 'tts_start':
      speaking = true; ui.talk.disabled = true; ui.stop.disabled = false; status('Speaking…')
      break
    case 'turn_complete':
      if (message.message === 'aborted') finishPlayback('Stopped')
      else audioQueue.then(() => finishPlayback('Ready')).catch(error => status(`Playback error: ${error.message}`))
      break
    case 'metrics': showMetrics(message.metrics); break
    case 'error':
      // The server discarded this recording; stop capturing instead of streaming into it.
      if (recording && message.code === 'bad_audio') abortRecording()
      speaking = false; ui.talk.disabled = false; ui.stop.disabled = true; status(`Error: ${message.message}`)
      break
  }
}

function finishPlayback(label) {
  speaking = false; ui.talk.disabled = false; ui.stop.disabled = true; status(label)
}

async function receiveAudio(buffer) {
  const view = new DataView(buffer), kind = view.getUint8(5), frameTurn = view.getUint32(8)
  if (frameTurn !== turn) return
  const wav = buffer.slice(16)
  if (kind === 3 || kind === 4) {
    const url = URL.createObjectURL(new Blob([wav], {type: 'audio/wav'}))
    debugURLs.push(url)
    if (kind === 3) ui.debugInputAudio.src = url
    else { ui.debugWhisperAudio.src = url; ui.debugDownload.href = url; ui.debugDownload.hidden = false }
    return
  }
  if (kind !== 2) return
  audioQueue = audioQueue.then(async () => {
    ctx ||= new AudioContext()
    const decoded = await ctx.decodeAudioData(wav)
    if (cancelledTurn === frameTurn) return
    await new Promise(resolve => {
      const player = ctx.createBufferSource()
      playing.push(player); player.buffer = decoded; player.connect(ctx.destination)
      player.onended = () => { playing = playing.filter(item => item !== player); resolve() }
      player.start()
      if (firstAudible === null && turnStoppedAt) {
        firstAudible = Math.round(performance.now() - turnStoppedAt)
        sendEvent('playback_started', {turn_id: turn, metrics: {first_audible_ms: firstAudible}})
        if (lastMetrics) showMetrics(lastMetrics)
      }
    })
  })
}

async function startRecording() {
  if (recording || starting || speaking || ws?.readyState !== WebSocket.OPEN) return
  starting = true; status('Preparing microphone…')
  try {
    ctx ||= new AudioContext()
    await ctx.resume()
    const selected = ui.microphone.value
    const constraints = {channelCount: 1, echoCancellation: false, noiseSuppression: false, autoGainControl: false}
    if (selected) constraints.deviceId = {exact: selected}
    stream = await navigator.mediaDevices.getUserMedia({audio: constraints})
    await populateMicrophones()
    await loadWorklet(ctx)
    if (!pressHeld) { cleanupCapture(); status('Ready — hold the button until “Listening” appears'); return }
    source = ctx.createMediaStreamSource(stream)
    turn++; cancelledTurn = 0; recording = true; stopping = false
    resetDebugRecording()
    sendEvent('listen_start', {turn_id: turn, language: ui.lang.value, sample_rate: ctx.sampleRate, capture_sample_rate: ctx.sampleRate})
    capture = startCapture({ctx, source, ws, turn,
      onLevel: peak => { ui.levelBar.style.width = `${Math.min(100, Math.round(peak * 100))}%` },
      onCongested: () => { status('Audio transport is congested; stopping safely'); requestStop() }})
    ui.talk.classList.add('recording'); ui.talk.textContent = 'Recording… release to send'; status(`Listening at ${ctx.sampleRate} Hz…`)
  } catch (error) {
    cleanupCapture(); status(`Microphone unavailable: ${error.message}`)
  } finally { starting = false }
}

async function requestStop() {
  if (!recording || stopping) return
  stopping = true; status('Finishing recording…')
  await capture.stop() // resolves after the final audio block has been sent
  finalizeStop()
}

function finalizeStop() {
  if (!recording) return
  recording = false; stopping = false
  cleanupCapture()
  turnStoppedAt = performance.now(); firstAudible = null; lastMetrics = null
  sendEvent('listen_stop', {turn_id: turn})
  ui.talk.classList.remove('recording'); ui.talk.textContent = 'Hold to talk'; ui.talk.disabled = true; status('Transcribing locally…')
}

function abortRecording() {
  recording = false; stopping = false
  cleanupCapture()
  ui.talk.classList.remove('recording'); ui.talk.textContent = 'Hold to talk'
}

function cleanupCapture() {
  capture?.abort()
  try { source?.disconnect() } catch {}
  stream?.getTracks().forEach(track => track.stop())
  stream = source = capture = null
}

async function populateMicrophones() {
  const current = ui.microphone.value
  const devices = (await navigator.mediaDevices.enumerateDevices()).filter(device => device.kind === 'audioinput')
  ui.microphone.innerHTML = '<option value="">System default</option>'
  devices.forEach((device, index) => ui.microphone.add(new Option(device.label || `Microphone ${index + 1}`, device.deviceId)))
  if ([...ui.microphone.options].some(option => option.value === current)) ui.microphone.value = current
}

function resetDebugRecording() {
  debugURLs.forEach(URL.revokeObjectURL); debugURLs = []; debugInfo = null
  ui.debugInputAudio.removeAttribute('src'); ui.debugWhisperAudio.removeAttribute('src'); ui.debugInputAudio.load(); ui.debugWhisperAudio.load()
  ui.debugDownload.hidden = true; ui.rawTranscript.textContent = '—'; ui.comparison.textContent = ''; ui.debugStats.textContent = 'Recording…'
}

function renderDebugStats() {
  if (!debugInfo) return
  const input = debugInfo.input, output = debugInfo.whisper
  ui.debugStats.textContent = [
    `Microphone: ${input.sample_rate} Hz, ${input.samples} samples, ${(input.duration_ms / 1000).toFixed(2)} s`,
    `Whisper: ${output.sample_rate} Hz, ${output.samples} samples, ${(output.duration_ms / 1000).toFixed(2)} s`,
    `Level: peak ${input.peak_dbfs.toFixed(1)} dBFS, RMS ${input.rms_dbfs.toFixed(1)} dBFS`,
    `Clipping: ${input.clipped_samples} samples (${input.clipped_percent.toFixed(3)}%)`,
    `Model: ${debugInfo.model}`,
    `Language sent to Whisper: ${debugInfo.language || ui.lang.value}`
  ].join('\n')
}

function normalizeText(value) { return value.toLocaleLowerCase().normalize('NFKC').replace(/[^\p{L}\p{N}\s]/gu, '').trim().replace(/\s+/g, ' ') }
function distance(a, b) {
  const row = Array.from({length: b.length + 1}, (_, i) => i)
  for (let i = 1; i <= a.length; i++) {
    let diagonal = row[0]; row[0] = i
    for (let j = 1; j <= b.length; j++) {
      const old = row[j]
      row[j] = Math.min(row[j] + 1, row[j - 1] + 1, diagonal + (a[i - 1] === b[j - 1] ? 0 : 1))
      diagonal = old
    }
  }
  return row[b.length]
}
function compareTranscript() {
  const expected = normalizeText(ui.expectedText.value), actual = normalizeText(ui.rawTranscript.textContent)
  if (!expected || !actual || actual === '—') { ui.comparison.textContent = 'Enter the expected sentence after recording to compare.'; return }
  const expectedWords = expected.split(' '), actualWords = actual.split(' ')
  const wer = 100 * distance(expectedWords, actualWords) / Math.max(1, expectedWords.length)
  const cer = 100 * distance([...expected], [...actual]) / Math.max(1, [...expected].length)
  ui.comparison.textContent = `Word error rate: ${wer.toFixed(1)}% · Character error rate: ${cer.toFixed(1)}%`
}

ui.talk.addEventListener('pointerdown', event => { event.preventDefault(); pressHeld = true; startRecording() })
window.addEventListener('pointerup', () => { pressHeld = false; requestStop() })
ui.talk.addEventListener('keydown', event => { if ((event.code === 'Space' || event.code === 'Enter') && !event.repeat) { pressHeld = true; startRecording() } })
ui.talk.addEventListener('keyup', event => { if (event.code === 'Space' || event.code === 'Enter') { pressHeld = false; requestStop() } })
ui.debugToggle.onchange = () => sendEvent('debug_enable', {debug: ui.debugToggle.checked})
ui.compareTranscript.onclick = compareTranscript
ui.expectedText.oninput = compareTranscript
ui.stop.onclick = () => { sendEvent('abort', {turn_id: turn}); cancelledTurn = turn; speaking = false; playing.forEach(player => { try { player.stop() } catch {} }); playing = []; audioQueue = Promise.resolve(); ui.stop.disabled = true; ui.talk.disabled = false }
ui.clear.onclick = () => { sendEvent('clear'); ui.user.textContent = 'Your transcript will appear here.'; ui.assistant.textContent = 'The reply will stream here.'; ui.user.classList.add('muted'); ui.assistant.classList.add('muted'); ui.metrics.innerHTML = '<span>Waiting for a completed turn</span>'; resetDebugRecording() }
ui.form.onsubmit = event => { event.preventDefault(); const text = ui.input.value.trim(); if (!text || speaking) return; turn++; cancelledTurn = 0; turnStoppedAt = performance.now(); firstAudible = null; lastMetrics = null; ui.input.value = ''; ui.assistant.textContent = ''; sendEvent('text_input', {turn_id: turn, text, language: ui.lang.value}); ui.talk.disabled = true }

function showMetrics(metrics) {
  lastMetrics = metrics
  const merged = {...metrics, ...(firstAudible === null ? {} : {first_audible_ms: firstAudible})}
  const labels = {stt_ms: 'STT', llm_first_text_ms: 'First text', llm_total_ms: 'LLM total', first_tts_ms: 'First TTS', first_audio_delivery_ms: 'Audio delivered', first_audible_ms: 'First audible', total_ms: 'Total'}
  ui.metrics.innerHTML = Object.entries(labels).filter(([key]) => merged[key] !== undefined).map(([key, label]) => `<div class="metric"><strong>${merged[key]} ms</strong><small>${label}</small></div>`).join('')
}

connect()
