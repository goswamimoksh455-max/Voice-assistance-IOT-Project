// Run with: node --test internal/server/webtest/
import test from 'node:test'
import assert from 'node:assert/strict'

// Minimal AudioWorklet and Web Audio stand-ins so the production modules load.
globalThis.AudioWorkletProcessor = class { constructor() { this.port = {postMessage() {}} } }
globalThis.registerProcessor = () => {}
const {toPCM16} = await import('../web/worklet.js')

test('Float32 to PCM16 conversion clamps and rounds like the Go tools', () => {
  const {pcm, peak, clippedSamples} = toPCM16([Float32Array.from([0, 0.5, -0.5, 1, -1, 1.5, -1.5, 0.25, NaN])])
  assert.deepEqual([...pcm], [0, 16384, -16384, 32767, -32768, 32767, -32768, 8192, 0])
  assert.equal(peak, 1.5)
  assert.equal(clippedSamples, 4)
})

test('multi-channel input is averaged to mono', () => {
  const {pcm} = toPCM16([Float32Array.from([0.5, 1, -1]), Float32Array.from([0.5, 0, 1])])
  assert.deepEqual([...pcm], [16384, 16384, 0])
})

class FakePort {
  constructor() { this.onmessage = null; this.posted = [] }
  postMessage(message) { this.posted.push(message) }
}
class FakeNode { connect() {} disconnect() {} }
globalThis.WebSocket = {OPEN: 1}
globalThis.AudioWorkletNode = class extends FakeNode {
  constructor() { super(); this.port = new FakePort(); FakeAudioWorkletNodes.push(this) }
}
const FakeAudioWorkletNodes = []
const fakeContext = () => ({createGain: () => Object.assign(new FakeNode(), {gain: {value: 1}}), destination: new FakeNode()})
const fakeSocket = () => ({readyState: 1, bufferedAmount: 0, sent: [], send(b) { this.sent.push(b) }})
const {frame, startCapture} = await import('../web/capture.js')

function decode(buffer) {
  const view = new DataView(buffer)
  const magic = String.fromCharCode(...new Uint8Array(buffer, 0, 4))
  return {magic, version: view.getUint8(4), kind: view.getUint8(5), turn: view.getUint32(8), sequence: view.getUint32(12), pcm: new Int16Array(buffer.slice(16))}
}

test('frame header matches the Go protocol (big-endian ids, LE PCM payload)', () => {
  const f = decode(frame(1, 0x01020304, 7, Int16Array.from([1, -2, 300]).buffer))
  assert.deepEqual({...f, pcm: [...f.pcm]}, {magic: 'VAP1', version: 1, kind: 1, turn: 0x01020304, sequence: 7, pcm: [1, -2, 300]})
})

test('capture sends ordered frames, flushes the final block on stop, then nothing more', async () => {
  const ws = fakeSocket()
  const capture = startCapture({ctx: fakeContext(), source: new FakeNode(), ws, turn: 9})
  const port = FakeAudioWorkletNodes.at(-1).port
  const block = n => ({data: {type: 'pcm', pcm: Int16Array.from({length: 128}, () => n), peak: 0, clippedSamples: 0}})
  port.onmessage(block(1)); port.onmessage(block(2))
  const stopping = capture.stop()
  assert.deepEqual(port.posted, [{type: 'stop'}])
  port.onmessage(block(3)) // last block emitted before the worklet acknowledged
  port.onmessage({data: {type: 'stopped'}})
  await stopping
  assert.equal(port.onmessage, null, 'stopped capture must not receive further audio')
  const frames = ws.sent.map(decode)
  assert.deepEqual(frames.map(f => [f.turn, f.sequence, f.pcm[0], f.pcm.length]), [[9, 0, 1, 128], [9, 1, 2, 128], [9, 2, 3, 128]])
})

test('consecutive captures use their own turn and restart sequence numbers', () => {
  const ws = fakeSocket()
  const first = startCapture({ctx: fakeContext(), source: new FakeNode(), ws, turn: 1})
  const firstPort = FakeAudioWorkletNodes.at(-1).port
  first.abort()
  startCapture({ctx: fakeContext(), source: new FakeNode(), ws, turn: 2})
  const secondPort = FakeAudioWorkletNodes.at(-1).port
  const message = {data: {type: 'pcm', pcm: new Int16Array(4), peak: 0}}
  firstPort.onmessage?.(message) // late block from the aborted capture
  secondPort.onmessage(message)
  assert.deepEqual(ws.sent.map(decode).map(f => [f.turn, f.sequence]), [[2, 0]])
})
