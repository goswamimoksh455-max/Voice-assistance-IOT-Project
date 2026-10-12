// Audio capture shared by the live microphone (app.js) and the development
// fixture replay (dev dashboard): any Web Audio source node → pcm-processor
// AudioWorklet → VAP1 binary frames on the WebSocket.

export const KIND_INPUT_PCM = 1
const MAX_BUFFERED_BYTES = 4 * 1024 * 1024
const STOP_ACK_TIMEOUT_MS = 750

export function frame(kind, id, sequence, payload) {
  const buffer = new ArrayBuffer(16 + payload.byteLength), view = new DataView(buffer)
  ;['V', 'A', 'P', '1'].forEach((char, i) => view.setUint8(i, char.charCodeAt(0)))
  view.setUint8(4, 1); view.setUint8(5, kind); view.setUint16(6, 0)
  view.setUint32(8, id); view.setUint32(12, sequence)
  new Uint8Array(buffer, 16).set(new Uint8Array(payload))
  return buffer
}

export function loadWorklet(ctx) {
  return ctx.audioWorklet.addModule('/web/worklet.js')
}

// startCapture streams `source` for one turn. The returned handle's stop()
// resolves after the worklet has flushed its final block; abort() drops the
// rest. Frames are bound to this capture's own node and turn, so audio from
// an earlier capture can never be sent as part of a later turn.
export function startCapture({ctx, source, ws, turn, onLevel, onCongested}) {
  const node = new AudioWorkletNode(ctx, 'pcm-processor')
  const silent = ctx.createGain()
  silent.gain.value = 0
  let sequence = 0, sending = true, acknowledged
  const stopped = new Promise(resolve => { acknowledged = resolve })
  node.port.onmessage = ({data}) => {
    if (data?.type === 'stopped') { acknowledged(); return }
    if (data?.type !== 'pcm' || !sending || ws.readyState !== WebSocket.OPEN) return
    if (ws.bufferedAmount > MAX_BUFFERED_BYTES) { sending = false; onCongested?.(); return }
    ws.send(frame(KIND_INPUT_PCM, turn, sequence++, data.pcm.buffer))
    onLevel?.(data.peak, data.clippedSamples)
  }
  source.connect(node); node.connect(silent); silent.connect(ctx.destination)
  const disconnect = () => {
    sending = false
    try { source.disconnect(node) } catch {}
    try { node.disconnect(); silent.disconnect() } catch {}
    node.port.onmessage = null
  }
  return {
    get frames() { return sequence },
    async stop() {
      node.port.postMessage({type: 'stop'})
      await Promise.race([stopped, new Promise(resolve => setTimeout(resolve, STOP_ACK_TIMEOUT_MS))])
      disconnect()
    },
    abort: disconnect,
  }
}
