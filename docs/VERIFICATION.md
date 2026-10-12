# Verification record

Date: 11 October 2026. Host: Apple M2 MacBook Air, ARM64, 8 CPU cores, 16 GB RAM, Metal enabled.

Installed and directly verified:

- Go 1.27.2 and CMake 4.4.4.
- whisper.cpp v1.9.4 at commit `927cfce`, compiled with embedded Metal, Accelerate, and ARM optimizations.
- Multilingual Whisper `base` (`ggml-base.bin`, 147.37 MB loaded model).
- Piper 1.8.0 with ONNX Runtime 1.23.2.
- `en_US-lessac-medium` and `hi_IN-priyamvada-medium`, both producing mono 16-bit 22,050 Hz WAV.
- Official Gemini Go SDK `google.golang.org/genai` v1.73.0.

Real local checks:

| Check | Result |
|---|---|
| Whisper bundled 11-second English JFK sample | Correct transcript |
| Warm Whisper HTTP transcription | 232 ms |
| Warm English Piper, “Hello professor.” | 281 ms; valid WAV |
| Warm Hindi Piper, “नमस्ते प्रोफेसर।” | 79 ms; valid WAV |
| First Whisper process lifetime including initial Metal shader compilation and two requests | 42.76 s |

These are point measurements, not statistically meaningful benchmarks. The first Whisper lifetime includes about 20 seconds of one-time Metal library compilation and is not equivalent to first-audio latency. Browser playback, Gemini, Hindi/Hinglish STT, contextual turns, rate limiting, and the full microphone-to-speaker pipeline were not executed because no `GEMINI_API_KEY` was present and no user speech corpus was supplied. No values are fabricated for those paths.

Automated verification completed successfully with `go test -race ./...`, `go vet ./...`, a clean build, shell syntax checks, and `git diff --check`.

## Update — speech-to-text evaluation (11 October 2026)

Measured on public FLEURS (English, Hindi) and OpenSLR 104 (Hinglish) recordings, through both
direct Whisper and the browser application path; see [STT evaluation](STT_EVALUATION.md) for the
method, before/after numbers and model comparison. Executed and passing: `go vet ./...`,
`VOICE_NETWORK_TESTS=1 go test -race ./...`, `node --test internal/server/webtest/capture.test.mjs`,
a 3-recording Whisper → Gemini → Piper smoke test (all speech WAVs decoded in the browser; first audio
2.1–2.6 s). Not executed by automation: the physical microphone, device selection and real
loudspeaker playback.
