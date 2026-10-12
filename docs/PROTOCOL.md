# WebSocket protocol v1

Connect at `/ws`. JSON text frames are control; binary frames are audio. A normal turn orders `listen_start`, microphone frames, `listen_stop`, `stt_result`, streamed `llm_delta`, ordered `tts_start` / WAV / `tts_end`, `metrics`, then `turn_complete`. `error` may end a stage. `abort` cancels work and queued playback.

## Binary frame

All header integers are big-endian; PCM samples are little-endian.

| Offset | Size | Field |
|---:|---:|---|
| 0 | 4 | ASCII `VAP1` |
| 4 | 1 | version `1` |
| 5 | 1 | kind: `1` input PCM16LE, `2` output WAV, `3` debug capture WAV, `4` debug Whisper WAV |
| 6 | 2 | reserved flags |
| 8 | 4 | turn ID |
| 12 | 4 | sequence number |

Input is mono signed PCM16LE at the AudioContext's actual sample rate, declared in `listen_start.sample_rate` (8–192 kHz). The server validates the complete recording and resamples it once to 16 kHz for Whisper; changing metadata without converting samples is never allowed. Sequences start at zero and are contiguous. `listen_stop` is sent only after the AudioWorklet confirms that previously queued audio has flushed. Default maximum duration is 30 seconds; recordings below 200 ms, malformed, oversized, or effectively silent are rejected.

Output is one complete RIFF/WAV sentence. The sample rate is in its header and `tts_start`. The client accepts only the current turn and plays sequence order.

Client events are `listen_start`, `listen_stop`, `abort`, `clear`, `text_input`, `playback_started`, and development-only `debug_enable`. The playback event reports browser-observed first-audible latency for structured logging. Server events are `hello`, `ready`, `stt_result`, `llm_delta`, `tts_start`, `tts_end`, `metrics`, `turn_complete`, `error`, and development-only `debug_state`, `audio_debug`, and `audio_debug_transcript`. Turn-scoped events carry `turn_id`.

Audio kinds 3 and 4 exist only on the originating WebSocket after both server-side `VOICE_AUDIO_DEBUG=true` and session opt-in. There is no debug HTTP endpoint or persistent recording. Future codecs can use negotiated extensions while retaining turn and sequence semantics.
