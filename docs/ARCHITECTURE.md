# Architecture

```text
Browser microphone → AudioWorklet → native-rate mono PCM16 → WebSocket → Go
  → validation + complete-utterance band-limited resampling to 16 kHz PCM16
  → whisper.cpp /inference (local Metal)
  → Gemini streaming text (cloud)
  → bounded sentence queue → warm Piper EN/HI (local)
  → ordered WAV frames → browser speaker
```

The Go server owns session state, cancellation, bounded history, provider calls, and credentials. Each connection has one active turn. Starting another cancels the prior context. A single reader and mutex-serialized writer protect each WebSocket.

Provider interfaces (`STT`, `LLM`, `TTS`) isolate orchestration. There is no database. History exists only in memory and is discarded on clear or disconnect.

Whisper and both Piper voices remain loaded. Gemini deltas reach the UI immediately; complete sentences enter a bounded synthesis queue while generation continues. Measurements cover STT, first text, LLM total, first synthesis, end-of-recording to first audio delivery, and total turn. Cold/warm benchmark values must come from actual runs and are never fabricated.

The browser deliberately does not resample. This keeps all conversion in one tested backend implementation and avoids per-AudioWorklet-block discontinuities. A stop/flush acknowledgement crosses the worklet message port before `listen_stop`, so every PCM message queued before release is delivered first. The worklet is connected through a zero-gain output node to keep the Web Audio graph alive without monitoring the microphone through the speakers.

## ESP32-S3 path

The versioned binary header carries kind, turn, sequence, and flags. A future client can negotiate Opus while retaining control events and cancellation. Hardware work still needs reconnect policy, constrained buffering, clock drift, and device authentication.
