#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
RUNTIME="$ROOT/runtime"
WHISPER="$RUNTIME/whisper.cpp"
PIPER_VENV="$RUNTIME/piper-venv"
mkdir -p "$RUNTIME" "$ROOT/models/whisper" "$ROOT/models/piper"

PIPER_PYTHON=${PIPER_PYTHON:-python3.10}
for tool in go cmake git curl "$PIPER_PYTHON"; do
  command -v "$tool" >/dev/null || { echo "Missing prerequisite: $tool" >&2; exit 1; }
done

if [[ ! -d "$WHISPER/.git" ]]; then
  git clone --branch v1.9.4 --depth 1 https://github.com/ggml-org/whisper.cpp.git "$WHISPER"
fi
cmake -S "$WHISPER" -B "$WHISPER/build" -DGGML_METAL=ON -DCMAKE_BUILD_TYPE=Release
cmake --build "$WHISPER/build" --config Release -j 4 --target whisper-server
if [[ ! -f "$ROOT/models/whisper/ggml-small.bin" ]]; then
  "$WHISPER/models/download-ggml-model.sh" small "$ROOT/models/whisper"
fi
# Hindi, Hinglish and auto-detection (see docs/STT_EVALUATION.md).
if [[ ! -f "$ROOT/models/whisper/ggml-large-v3-turbo-q5_0.bin" ]]; then
  "$WHISPER/models/download-ggml-model.sh" large-v3-turbo-q5_0 "$ROOT/models/whisper"
fi
# base is faster but inaccurate for Hindi; download it only for comparisons.
if [[ "${WHISPER_DOWNLOAD_BASE:-0}" == "1" && ! -f "$ROOT/models/whisper/ggml-base.bin" ]]; then
  "$WHISPER/models/download-ggml-model.sh" base "$ROOT/models/whisper"
fi

if [[ ! -x "$PIPER_VENV/bin/pip" ]]; then "$PIPER_PYTHON" -m venv "$PIPER_VENV"; fi
"$PIPER_VENV/bin/pip" install --upgrade 'piper-tts[http]==1.8.0'
(
  cd "$ROOT/models/piper"
  "$PIPER_VENV/bin/python" -m piper.download_voices en_US-lessac-medium hi_IN-priyamvada-medium
)

cd "$ROOT"
go mod download
go build -o bin/professor-assistant ./cmd/professor-assistant
echo "Setup complete. Copy .env.example to .env, add GEMINI_API_KEY, then run scripts/start-local.sh"
