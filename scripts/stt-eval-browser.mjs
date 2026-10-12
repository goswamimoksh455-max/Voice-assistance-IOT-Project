#!/usr/bin/env node
// Runs the STT evaluation dashboard (/dev/) in headless Chrome and prints the
// saved report path and summary. Requires a server started with
// VOICE_DEV_TOOLS=true and fixtures from `go run ./cmd/fetch-fixtures`.
//
//   node scripts/stt-eval-browser.mjs [--url http://127.0.0.1:8090] [--language en|hi|hinglish]
//        [--dataset FLEURS|OpenSLR-104] [--count 20] [--rate 16000|44100|48000] [--label name]
//        [--whisperLanguage auto|en|hi] [--mode stt|full] [--chrome /path/to/chrome]
import {spawn} from 'node:child_process'
import {mkdtempSync, rmSync} from 'node:fs'
import {tmpdir} from 'node:os'
import {join} from 'node:path'
import {parseArgs} from 'node:util'

const {values: opts} = parseArgs({options: {
  url: {type: 'string', default: 'http://127.0.0.1:8090'}, language: {type: 'string'}, dataset: {type: 'string'}, count: {type: 'string'},
  rate: {type: 'string'}, label: {type: 'string'}, whisperLanguage: {type: 'string'}, mode: {type: 'string'},
  chrome: {type: 'string', default: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'}, timeout: {type: 'string', default: '3600'},
}})
const query = new URLSearchParams({autorun: '1'})
for (const key of ['language', 'dataset', 'count', 'rate', 'label', 'whisperLanguage', 'mode']) if (opts[key]) query.set(key, opts[key])
const page = `${opts.url}/dev/?${query}`
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms))

const profile = mkdtempSync(join(tmpdir(), 'stt-eval-chrome-'))
const port = 9400 + Math.floor(Math.random() * 500)
const chrome = spawn(opts.chrome, ['--headless=new', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`,
  '--autoplay-policy=no-user-gesture-required', '--no-first-run', '--no-default-browser-check', 'about:blank'], {stdio: 'ignore'})
const cleanup = () => { chrome.kill(); try { rmSync(profile, {recursive: true, force: true}) } catch {} }
process.on('exit', cleanup)
process.on('SIGINT', () => process.exit(130))

let targets
for (let i = 0; i < 100 && !targets; i++) {
  try { targets = await (await fetch(`http://127.0.0.1:${port}/json`)).json() } catch { await sleep(100) }
}
if (!targets) { console.error('Chrome did not start'); process.exit(1) }
const socket = new WebSocket(targets.find(t => t.type === 'page').webSocketDebuggerUrl)
await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject })
let id = 0
const pending = new Map()
socket.onmessage = ({data}) => {
  const message = JSON.parse(data)
  if (message.id && pending.has(message.id)) { pending.get(message.id)(message); pending.delete(message.id) }
  if (message.method === 'Runtime.exceptionThrown') console.error('page exception:', message.params.exceptionDetails.exception?.description ?? message.params.exceptionDetails.text)
}
const cdp = (method, params = {}) => new Promise(resolve => { const i = ++id; pending.set(i, resolve); socket.send(JSON.stringify({id: i, method, params})) })
const evaluate = async expression => (await cdp('Runtime.evaluate', {expression, returnByValue: true})).result?.result?.value

await cdp('Runtime.enable')
await cdp('Page.navigate', {url: page})
console.error(`running ${page}`)
const deadline = Date.now() + Number(opts.timeout) * 1000
let last = ''
while (Date.now() < deadline) {
  const done = await evaluate('window.__evalDone')
  if (done) {
    if (done.error) { console.error(done.error); process.exit(1) }
    console.log(JSON.stringify(done, null, 2))
    process.exit(0)
  }
  const state = await evaluate(`document.getElementById('state')?.textContent`)
  if (state && state !== last) { console.error(state); last = state }
  await sleep(500)
}
console.error('timed out')
process.exit(1)
