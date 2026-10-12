#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
RUN="$ROOT/runtime/run"
LOG="$ROOT/runtime/log"
mkdir -p "$RUN" "$LOG"
if [[ -f "$ROOT/.env" ]]; then set -a; source "$ROOT/.env"; set +a; fi
: "${GEMINI_API_KEY:?Set GEMINI_API_KEY in $ROOT/.env before starting services}"
cd "$ROOT"

WHISPER_MODEL=${WHISPER_MODEL:-models/whisper/ggml-small.bin}
# Hindi, Hinglish and auto-detection use a second, more accurate model.
# Set WHISPER_MULTILINGUAL_URL= (empty) in .env to run a single server.
WHISPER_MULTILINGUAL_URL=${WHISPER_MULTILINGUAL_URL-http://127.0.0.1:8082}
WHISPER_MULTILINGUAL_MODEL=${WHISPER_MULTILINGUAL_MODEL:-models/whisper/ggml-large-v3-turbo-q5_0.bin}
[[ -z "$WHISPER_MULTILINGUAL_URL" ]] || [[ -e "$WHISPER_MULTILINGUAL_MODEL" ]] || { echo "Missing $WHISPER_MULTILINGUAL_MODEL; run scripts/setup-local.sh first" >&2; exit 1; }
for required in \
  "$ROOT/runtime/whisper.cpp/build/bin/whisper-server" \
  "$WHISPER_MODEL" \
  "$ROOT/runtime/piper-venv/bin/python"; do
  [[ -e "$required" ]] || { echo "Missing $required; run scripts/setup-local.sh first" >&2; exit 1; }
done

# port_of extracts the port from a service URL in .env, so each local service
# listens exactly where the assistant will look for it.
port_of() {
  local hostport=${1#*://}
  hostport=${hostport%%/*}
  if [[ "$hostport" != *:* || ! "${hostport##*:}" =~ ^[0-9]+$ ]]; then
    echo "$2 must include an explicit port, e.g. http://127.0.0.1:8081 (got '$1')" >&2
    exit 1
  fi
  echo "${hostport##*:}"
}
WHISPER_PORT=$(port_of "${WHISPER_URL:-http://127.0.0.1:8081}" WHISPER_URL)
PIPER_EN_PORT=$(port_of "${PIPER_EN_URL:-http://127.0.0.1:5000}" PIPER_EN_URL)
PIPER_HI_PORT=$(port_of "${PIPER_HI_URL:-http://127.0.0.1:5001}" PIPER_HI_URL)
[[ -z "$WHISPER_MULTILINGUAL_URL" ]] || WHISPER_MULTILINGUAL_PORT=$(port_of "$WHISPER_MULTILINGUAL_URL" WHISPER_MULTILINGUAL_URL)

start_one() {
  local name=$1; shift
  local pidfile="$RUN/$name.pid" cmdfile="$RUN/$name.cmd"
  if [[ -f "$pidfile" ]] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then
    # A running process keeps executing its old code and settings, so restart it
    # when its executable was rebuilt or its command line (model, voice) changed.
    if [[ "$*${STAMP:-}" == "$(cat "$cmdfile" 2>/dev/null)" && ! "$1" -nt "$pidfile" ]]; then echo "$name is already running"; return; fi
    echo "Restarting $name (binary or configuration changed)"
    kill "$(cat "$pidfile")"
    for _ in 1 2 3 4 5 6 7 8 9 10; do kill -0 "$(cat "$pidfile")" 2>/dev/null || break; sleep 0.3; done
  fi
  nohup "$@" >"$LOG/$name.log" 2>&1 &
  echo $! >"$pidfile"
  echo "$*${STAMP:-}" >"$cmdfile"
	sleep 0.2
	if ! kill -0 "$!" 2>/dev/null; then
	  echo "$name failed to start; inspect $LOG/$name.log" >&2
	  return 1
	fi
  echo "Started $name (PID $!)"
}

start_one whisper "$ROOT/runtime/whisper.cpp/build/bin/whisper-server" --host 127.0.0.1 --port "$WHISPER_PORT" --model "$WHISPER_MODEL" --language auto
if [[ -n "$WHISPER_MULTILINGUAL_URL" ]]; then
  start_one whisper-multi "$ROOT/runtime/whisper.cpp/build/bin/whisper-server" --host 127.0.0.1 --port "$WHISPER_MULTILINGUAL_PORT" --model "$WHISPER_MULTILINGUAL_MODEL" --language auto
elif [[ -f "$RUN/whisper-multi.pid" ]] && kill -0 "$(cat "$RUN/whisper-multi.pid")" 2>/dev/null \
  && ps -p "$(cat "$RUN/whisper-multi.pid")" -o command= | grep -q whisper-server; then
  kill "$(cat "$RUN/whisper-multi.pid")"; rm -f "$RUN/whisper-multi.pid" "$RUN/whisper-multi.cmd"
  echo "Stopped whisper-multi (WHISPER_MULTILINGUAL_URL is empty)"
fi
start_one piper-en "$ROOT/runtime/piper-venv/bin/python" -m piper.http_server -m "${PIPER_EN_VOICE:-en_US-lessac-medium}" --data-dir "$ROOT/models/piper" --host 127.0.0.1 --port "$PIPER_EN_PORT"
start_one piper-hi "$ROOT/runtime/piper-venv/bin/python" -m piper.http_server -m "${PIPER_HI_VOICE:-hi_IN-priyamvada-medium}" --data-dir "$ROOT/models/piper" --host 127.0.0.1 --port "$PIPER_HI_PORT"
# Always rebuild: the web UI is embedded, so a stale binary also serves stale JavaScript.
# The binary is replaced only when its contents change, which triggers a restart above.
go build -o bin/professor-assistant.new ./cmd/professor-assistant
if cmp -s bin/professor-assistant.new bin/professor-assistant; then rm bin/professor-assistant.new; else mv bin/professor-assistant.new bin/professor-assistant; fi
# The assistant reads its settings from the environment, so .env edits also restart it.
STAMP=" env:$(cat "$ROOT/.env" 2>/dev/null | shasum | cut -c1-12)" start_one assistant "$ROOT/bin/professor-assistant"
echo "Open http://127.0.0.1:8090"
