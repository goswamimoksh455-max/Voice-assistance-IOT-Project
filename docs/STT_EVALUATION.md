# Speech-to-text evaluation

Development-only tooling that measures transcription accuracy on public speech
recordings with verified reference transcripts. Nothing here runs in normal
operation: the dashboard and its endpoints exist only when `VOICE_DEV_TOOLS=true`
and answer localhost clients only.

## Datasets

| Set | Source | Licence | Selection |
|---|---|---|---|
| English | [Google FLEURS](https://huggingface.co/datasets/google/fleurs) `en_us` test, revision `70bb2e84b976b7e960aa89f1c648e09c59f894dd` | CC BY 4.0 | First N unique sentences in archive order |
| Hindi | FLEURS `hi_in` test, same revision | CC BY 4.0 | First N unique sentences in archive order |
| Hinglish | [OpenSLR 104](https://openslr.org/104/) `Hindi-English_test.tar.gz` (MUCS 2021 spoken tutorials) | CC BY-SA 4.0 | Code-switched segments (Devanagari + Latin words, 2–15 s) from the first 3 recordings in the archive, spread evenly, ordered by utterance id |

`cmd/fetch-fixtures` streams the archives and stops as soon as it has what it
needs. FLEURS needs a few MB per language. For OpenSLR 104 the Kaldi metadata
sits about 230 MB into the 443 MB archive, so roughly half of it is streamed; only
the selected segments (about 2.5 MB) and the `text`/`segments` files are kept.
Archive entry names are validated and only sanitized base names are written, so a
malicious archive cannot write outside the fixture directory.

Every fixture is stored as 16 kHz mono PCM16 WAV. FLEURS ships 32-bit float
WAV; it is converted with the same clamp-and-round rule as the browser
AudioWorklet (`audio.FloatToPCM16`, `worklet.js toPCM16`). `runtime/fixtures/manifest.json`
records the source URL, revision, licence, original file and format, reference and
raw transcript, duration and SHA-256 of every fixture. Rejected candidates are listed
with a reason. Only digital silence (peak below −60 dBFS) is rejected, because quiet
speech is a valid test case. Fixtures live under `runtime/`, which Git ignores.

```sh
go run ./cmd/fetch-fixtures -en 20 -hi 20                 # FLEURS only, ~14 MB
go run ./cmd/fetch-fixtures -en 20 -hi 20 -hinglish 10    # also streams OpenSLR 104
```

Re-running reuses the cache and only adds missing fixtures. Raise `-en`, `-hi`
or `-hinglish` to add more. Because selection is deterministic, a larger set
always contains the smaller one.

## Two paths per recording

- **Direct**: the fixture WAV is sent unchanged to whisper-server by
  `stt.Whisper.TranscribeWAV`, with exactly the request fields used for live
  audio. This is the baseline for what Whisper itself can recognize.
- **Application**: the dashboard decodes the WAV in the browser
  (`decodeAudioData` resamples it to the AudioContext rate), plays it through an
  `AudioBufferSourceNode` into the production capture module (`capture.js` →
  `worklet.js` → VAP1 WebSocket frames). It stops exactly like releasing the talk
  button, and the server runs its normal validation, resampling and Whisper request.
  In STT-only mode the server stops after Whisper and never calls Gemini or Piper.
  The server also aligns the 16 kHz audio it sent to Whisper with the original
  fixture and reports lag, correlation, SNR and duration difference
  (`audio.Align`).

Not exercised identically to a microphone: `getUserMedia`, device selection, the
OS/browser capture path (no echo cancellation, noise suppression or AGC are
requested anyway), and real-time clock drift between microphone and
AudioContext. The browser resampler (16 kHz → context rate) is exercised; a
physical microphone delivers audio at the hardware rate instead.

## Scoring

`internal/eval`: Unicode NFC and lower case; punctuation, symbols and the danda
are removed and whitespace collapsed. Scripts are never transliterated, so Latin
Hindi does not match a Devanagari reference and Urdu-script output counts as
wrong. WER and CER come from a minimum edit-distance alignment, with
substitution, deletion and insertion counts. Each transcript's dominant script is
reported, and a script mismatch is flagged (for example Arabic script for Hindi).
For Hinglish, Latin-script reference words, mostly English technical terms, are
counted separately as "technical words recognized" when they appear verbatim in
Latin script. FLEURS English references spell numbers as Whisper does ("25 to
30"), but other normalization differences (British/American spelling, "Ms" vs
"Miss") still count as errors. No Whisper text normalizer is applied.

Dashboard status: **pass** when application WER ≤ 15%, there is no script mismatch,
audio correlation ≥ 0.98 and the direct and application transcripts are identical.
**Needs review** when WER is ≤ 40% or the two paths differ. **Failure** otherwise, or on
any error. Meaning alone never counts as a pass.

## Running

```sh
# 1. Fixtures (once)
go run ./cmd/fetch-fixtures -en 20 -hi 20 -hinglish 10

# 2. A server with the dashboard. Either set VOICE_DEV_TOOLS=true in .env and run
#    ./scripts/start-local.sh, or start a second instance next to the normal one:
VOICE_ADDR=127.0.0.1:8092 VOICE_DEV_TOOLS=true ./bin/professor-assistant   # with .env exported

# 3a. Interactive: open http://127.0.0.1:8092/dev/
# 3b. Headless, repeatable (Chrome required), report saved to runtime/eval/:
node scripts/stt-eval-browser.mjs --url http://127.0.0.1:8092 --language hi --count 20 --label after-hi
node scripts/stt-eval-browser.mjs --url http://127.0.0.1:8092 --rate 16000 --count 5 --label rate-16k
```

The dashboard selects dataset, language, Whisper language override, sample count,
AudioContext rate (browser default, 16, 44.1 or 48 kHz) and mode. It can run one
recording or all, stop a batch, save the report on the server, and download JSON or
CSV. Selecting a row plays the original recording and the exact WAV submitted to
Whisper. **Full pipeline smoke** mode runs at most three recordings through Gemini
and Piper and checks that the returned speech WAVs decode in the browser.

## Comparing models and decoding options

`cmd/stt-bench` runs the direct path only, which is faster and needs no browser.
Use it for model and parameter comparisons once the application path has been
shown to match direct Whisper:

```sh
go run ./cmd/stt-bench -label small-default
go run ./cmd/stt-bench -label small-beam5 -options beam_size=5,temperature_inc=0
# another model on another port:
runtime/whisper.cpp/build/bin/whisper-server --port 8082 --model models/whisper/ggml-base.bin
go run ./cmd/stt-bench -whisper-url http://127.0.0.1:8082 -label base
go run ./cmd/stt-bench -compare runtime/eval/<a>.json,runtime/eval/<b>.json
```

The application applies the same options through `WHISPER_OPTIONS`.

## Live microphone

Public recordings passing does not prove the live microphone works. To score a
real session with the same tools:

1. Set `VOICE_AUDIO_DEBUG=true`, enable **Audio Debug** on the main page, speak,
   and click **Download Whisper WAV**. That file is exactly what reached Whisper.
2. Add it as a private fixture, with what you actually said:
   ```sh
   go run ./cmd/fetch-fixtures -add ~/Desktop/voice-clips/hi-01.wav -add-language hi -reference "कल सुबह साढ़े दस बजे मेरी क्लास है"
   ```
3. Run the dashboard or `stt-bench`. The recording appears with source `local`
   and stays under `runtime/` (never committed).

If the direct transcript of that WAV is good but live turns are bad, the problem
is upstream of Go (microphone, device selection or browser capture). Compare the
debug panel's "Exact backend input" with what you said, checking duration, level and
clipping.

## Results (11 October 2026, Apple M2, 16 GB, Metal)

Application path = browser replay through the production capture code at the
browser's default AudioContext rate. Failed or rejected turns count as 100% WER.
The same 50 public recordings were used in every run.

| Run | Language | Direct WER | App WER | App CER | App failures | App STT median / p95 |
|---|---|---|---|---|---|---|
| Before fixes (small) | English (20) | 6.8% | 62.4% | 61.3% | 12 | 539 / 648 ms |
| After speech-gate fix (small) | English | 6.8% | 6.7% | 3.7% | 0 | 610 / 3409 ms |
| **Final (routing)** | English | 6.8% | **7.2%** | **3.8%** | 0 | **518 / 691 ms** |
| Before fixes (small) | Hindi (20) | 58.5% | 62.3% | 35.9% | 2 | 1198 / 9106 ms |
| After speech-gate fix (small) | Hindi | 58.5% | 57.9% | 28.4% | 0 | 1286 / 2962 ms |
| **Final (routing → large-v3-turbo)** | Hindi | 30.8% | **31.0%** | **9.9%** | 0 | **1879 / 2227 ms** |
| Before fixes (small) | Hinglish (10) | 121.9% | 90.1% | 83.3% | 0 | 10015 / 17626 ms |
| **Final (routing → large-v3-turbo)** | Hinglish | 67.4% | **67.4%** | **58.6%** | 0 | **1664 / 1713 ms** |

Direct WER differs between rows only because the final configuration routes
Hindi and Hinglish to large-v3-turbo. Application and direct transcripts agree
closely in every run; the minimum audio correlation is 0.979 (English, a very
quiet recording) and 0.994–0.9997 otherwise, with 0 ms lag.

Hinglish technical words in Latin script: 0/31 before, 9/31 final. Most errors
are English terms written in Devanagari ("मशीन लेर्निंग"). They are scored as
wrong on purpose, but Gemini understands them.

### Sample rates (application path, 3 English + 3 Hindi)

| AudioContext | Duration difference | Lag | Correlation | SNR | Transcripts vs direct |
|---|---|---|---|---|---|
| 16 kHz | 0–4 ms | 0 ms | 1.0000 | bit-identical | identical |
| 44.1 kHz | ≤ 2 ms | 0 ms | 0.9953–1.0000 | 20–67 dB | WER within ±3 points |
| 48 kHz | ≤ 1 ms | 0 ms | 0.9953–1.0000 | 20–67 dB | WER within ±3 points |

The 20 dB case is the −62 dBFS recording, where 16-bit rounding noise is a
large share of the signal.

### Models and engines (direct path, same 50 recordings)

| Engine + model | English WER, median | Hindi WER / CER, median (p95) | Hinglish WER | Memory |
|---|---|---|---|---|
| whisper.cpp base | 12.4%, 151 ms | 102.8% / 104.7%, 761 ms; Urdu script 20/20 | 99.4% | 256 MB |
| whisper.cpp small | 6.8%, 394 ms | 58.5% / 27.6%, 4280 ms (9951) | 121.9% | 737 MB |
| faster-whisper small (CPU int8) | 6.2%, 1774 ms | 59.9% / 30.6%, 7617 ms (57785) | 94.3% | 449 MB |
| faster-whisper large-v3-turbo (CPU int8) | 5.8%, 7166 ms | 30.9% / 10.0%, 11082 ms (30810) | 80.0% | 991 MB |
| **whisper.cpp large-v3-turbo q5_0** | 4.9%, 1576 ms | **30.8% / 9.6%, 2318 ms (2680)** | **67.4%** | 664 MB |

faster-whisper (CTranslate2) has no Apple GPU backend on macOS, so it runs on the
CPU and was 4–5× slower at equal accuracy. whisper.cpp small decoding options
were also tested: disabling temperature fallback caused repetition loops
(Hinglish WER 300%), and beam search (5) did not improve Hindi and tripled
English latency, so the default decoding is kept.

**Chosen configuration:** English → small (fast), Hindi/Hinglish/Auto →
large-v3-turbo, both whisper.cpp with Metal (about 1.4 GB together).

### Bugs found by this evaluation

1. **Quiet speech rejected** (`audio.ValidatePCM16`): a fixed RMS threshold of
   90 (about −51 dBFS) answered "no speech detected" to 12/20 English and 2/20
   Hindi recordings that Whisper transcribes correctly. Replaced by a
   level-spread gate (`audio.HasSpeech`). It was checked on room noise from real
   recordings, where a gate is needed because Whisper hallucinates on noise.
2. **Empty WebSocket events**: `client.send` ignored JSON encoding errors, so an
   unencodable value (+Inf SNR on bit-identical 16 kHz audio) reached the
   browser as an empty frame. Errors are now logged and reported, and the SNR is
   capped.
3. **Runaway decoding with small on Hindi/Hinglish**: temperature fallback took
   up to 48 s on a 2 s clip and one request hit the 60 s STT timeout. Resolved by
   routing those languages to large-v3-turbo (p95 1.7–2.7 s, no timeouts).

### Remaining limitations

- Hindi WER is still about 31% on FLEURS (formal read news vocabulary; CER about 10%).
  Hinglish English terms are mostly written in Devanagari.
- OpenSLR 104 segment times are whole seconds, so some clips cut words at the
  edges, which affects both paths equally.
- Synthetic Piper speech and public datasets do not replace live-microphone
  testing. Score real recordings with `fetch-fixtures -add`.
