# Professor Voice Assistant — Phase 0 + Phase 3

A localhost-only, push-to-talk voice assistant for English, Hindi, and Hinglish. The browser captures audio, the Go server coordinates a persistent local whisper.cpp service, streams text from Gemini, and begins sentence-level synthesis through two warm local Piper services.

> Repository note: `PLAN.md` appeared after the initial inspection and was then reviewed without modification. This implements its Phase 0 laptop prototype plus Phase 3 local STT/TTS; no separate blueprint document was present.

## Requirements

Validated target: Apple M2 (ARM64), macOS 26.1, 16 GB RAM, Metal supported. The setup uses whisper.cpp v1.9.4, multilingual `ggml-small.bin` (English) and `ggml-large-v3-turbo-q5_0.bin` (Hindi, Hinglish, Auto), Piper 1.8.0, `en_US-lessac-medium`, and `hi_IN-priyamvada-medium`. Go 1.24+ and Python 3.10 are required. Python 3.10 is intentional: pip bundled with the detected Homebrew Python 3.12 fails to identify macOS 26.

## Setup and run

```sh
brew install go cmake python@3.10
./scripts/setup-local.sh
cp .env.example .env
# Edit .env and set GEMINI_API_KEY. Never commit this file.
./scripts/start-local.sh
```

Open <http://127.0.0.1:8090>. Use the text box to validate Gemini and Piper without microphone access. Check readiness with `curl -i http://127.0.0.1:8090/readyz`. Stop only the processes started by this project with `./scripts/stop-local.sh`.

Only `GEMINI_API_KEY` is secret and required from you. The local Whisper and Piper settings already have defaults. `.env` is intentionally hidden in Finder and many editors because its name starts with a dot; create it with `cp .env.example .env`, then enable hidden files in Finder with `Command-Shift-.` if needed.

The setup clones the pinned whisper.cpp release, builds with Metal, downloads the multilingual small and large-v3-turbo models, creates a Python 3.10 virtual environment, installs pinned Piper, and downloads both voices. Models, logs, PIDs, binaries, and `.env` are ignored by Git.

## Configuration and tests

All settings are in `.env.example`. The backend exclusively reads `GEMINI_API_KEY`; it is never exposed or logged. `gemini-3.5-flash-lite` with `minimal` thinking is the default. `gemini-3.8-flash` is an explicit alternative and requires `low`, `medium`, or `high`; invalid `minimal` use is rejected. No paid fallback occurs.

```sh
go test ./...
go vet ./...
go build ./cmd/professor-assistant
VOICE_NETWORK_TESTS=1 go test -race ./internal/server
node --test internal/server/webtest/capture.test.mjs   # browser capture/worklet unit tests
```

Unit tests cover framing, PCM conversion, the speech gate, resampling, WAV encoding, WebSocket reception and stop handling, bounded history, cancellation, sentence splitting, Whisper request parameters, and voice routing. Real integration checks require downloaded models and a user-provided Gemini key; mock tests are never represented as real inference.

## Behavior and limitations

- The AudioWorklet emits mono PCM16 at the browser AudioContext's actual rate (commonly 44.1 or 48 kHz). Go validates and resamples the complete utterance once to 16 kHz with a band-limited converter before transcription. Whisper HTTP is not presented as incremental streaming.
- TTS starts on the first completed sentence while Gemini continues streaming. Turn and sequence IDs reject stale or reordered audio.
- Microphone capture is disabled while speech plays. Stop cancels generation and playback. Full duplex, wake words, RAG, web search, and ESP32 firmware are excluded.
- Hinglish uses the Hindi voice. Mixed-script pronunciation is expected to be less natural. The Hinglish setting is transcribed as Hindi (`hi`). English goes to the small model for speed; Hindi, Hinglish and Auto go to large-v3-turbo on a second whisper.cpp process (`WHISPER_MULTILINGUAL_URL`), which roughly halves Hindi errors. Both run on the Mac GPU (Metal) and use about 1.4 GB together.
- Speech-to-text accuracy is measured on public English, Hindi and Hinglish recordings with a development dashboard; see [STT evaluation](docs/STT_EVALUATION.md) for the method, results and how to rerun them.
- Set `VOICE_AUDIO_DEBUG=true`, restart the app, and explicitly enable Audio Debug in the browser to inspect the exact backend input and exact WAV submitted to Whisper. Debug audio is session-only and is not written to disk. See [audio debugging](docs/AUDIO_DEBUGGING.md).
- `/healthz` checks Go. `/readyz` checks local speech services. Gemini credentials are exercised only by a real request.
- Latencies shown are real stage timings. No benchmark values are included until real inference runs.

See [architecture](docs/ARCHITECTURE.md) and [protocol](docs/PROTOCOL.md).
The exact machine-local checks and honest unverified paths are recorded in [verification](docs/VERIFICATION.md).
