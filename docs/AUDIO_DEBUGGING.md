# Audio debugging

Audio inspection is deliberately off by default. Set `VOICE_AUDIO_DEBUG=true` in `.env`, restart the local services, open the browser, and then enable **Audio Debug** for that browser session. A recording produces two players:

- **Exact backend input** is the mono PCM16 stream received over the WebSocket, wrapped in a WAV header using the declared browser sample rate.
- **Exact 16 kHz Whisper input** is the post-validation, post-resampling WAV sent to whisper.cpp.

Listen to both. Speech speed, pitch, and duration should match. The panel reports sample counts, duration, peak/RMS levels, clipping, selected model, language sent to Whisper, and the unmodified Whisper transcript. Enter the phrase you actually spoke to calculate simple WER and CER. The download button exports the exact Whisper WAV for direct comparison.

No debug audio is placed in a server file or public endpoint. The current browser holds bounded object URLs in memory; starting another recording or clearing the conversation revokes them. Turning the checkbox off stops sending debug copies for later recordings.

## Direct-versus-pipeline comparison

Download `whisper-input.wav` from the panel. A direct whisper.cpp request is:

```sh
curl -sS http://127.0.0.1:8081/inference \
  -F file=@whisper-input.wav \
  -F temperature=0 \
  -F translate=false \
  -F response_format=verbose_json \
  -F language=auto
```

Run the normal pipeline with the same file:

```sh
go run ./cmd/e2e-check -file whisper-input.wav -language auto
```

If the raw transcripts match, transport and conversion are not the differentiator. If they differ, retain both WAVs and compare their reported sample count and duration before changing models.

Whisper models and decoding options are compared on public recordings with
`cmd/stt-bench`; see [STT evaluation](STT_EVALUATION.md). To add the faster but
less accurate base model for comparison:

```sh
WHISPER_DOWNLOAD_BASE=1 ./scripts/setup-local.sh
```
