// Mixes all input channels to mono and converts Float32 samples to PCM16 with
// the same rounding the Go evaluation tools use (audio.FloatToPCM16).
export function toPCM16(channels) {
  const length = channels[0].length
  const out = new Int16Array(length)
  let peak = 0, clippedSamples = 0
  for (let i = 0; i < length; i++) {
    let sample = 0
    for (const channel of channels) sample += channel[i]
    sample /= channels.length
    if (Number.isNaN(sample)) sample = 0
    peak = Math.max(peak, Math.abs(sample))
    if (sample >= 1 || sample <= -1) clippedSamples++
    const clipped = Math.max(-1, Math.min(1, sample))
    out[i] = clipped < 0 ? Math.round(clipped * 32768) : Math.round(clipped * 32767)
  }
  return {pcm: out, peak, clippedSamples}
}

class PCMProcessor extends AudioWorkletProcessor {
  constructor() {
    super()
    this.active = true
    this.port.onmessage = event => {
      if (event.data?.type === 'stop' && this.active) {
        this.active = false
        // Messages from this port are ordered, so all earlier PCM reaches the
        // main thread before this acknowledgement.
        this.port.postMessage({type: 'stopped'})
      }
    }
  }
  process(inputs) {
    const channels = inputs[0]
    if (!this.active) return false
    if (!channels?.length) return true
    const {pcm, peak, clippedSamples} = toPCM16(channels)
    this.port.postMessage({type: 'pcm', pcm, peak, clippedSamples}, [pcm.buffer])
    return true
  }
}
registerProcessor('pcm-processor', PCMProcessor)
