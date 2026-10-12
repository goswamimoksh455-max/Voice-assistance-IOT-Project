#!/usr/bin/env python3
"""Local faster-whisper service exposing whisper.cpp's whisper-server API.

POST /inference (multipart: file=<16-bit mono WAV>, language=en|hi|auto, ...)
returns {"text": ..., "language": ...}, so the Go STT client and
cmd/stt-bench work unchanged. Used to compare engines on identical audio.

  runtime/fw-venv/bin/python scripts/faster_whisper_server.py --model small --port 8083
"""
import argparse
import io
import json
import time
import wave
from email.parser import BytesParser
from email.policy import HTTP
from http.server import BaseHTTPRequestHandler, HTTPServer

import numpy as np
from faster_whisper import WhisperModel


def parse_multipart(content_type, body):
    message = BytesParser(policy=HTTP).parsebytes(b"Content-Type: " + content_type.encode() + b"\r\n\r\n" + body)
    fields = {}
    for part in message.iter_parts():
        name = part.get_param("name", header="content-disposition")
        if name:
            fields[name] = part.get_payload(decode=True)
    return fields


def read_wav(data):
    with wave.open(io.BytesIO(data)) as w:
        if w.getnchannels() != 1 or w.getsampwidth() != 2:
            raise ValueError("expected 16-bit mono WAV")
        if w.getframerate() != 16000:
            raise ValueError("expected 16 kHz WAV (the application always resamples)")
        return np.frombuffer(w.readframes(w.getnframes()), dtype="<i2").astype(np.float32) / 32768.0


class Handler(BaseHTTPRequestHandler):
    model = None
    options = {}

    def do_GET(self):
        self.reply(200, {"status": "ok"})

    def do_POST(self):
        if self.path != "/inference":
            self.reply(404, {"error": "not found"})
            return
        try:
            body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
            fields = parse_multipart(self.headers["Content-Type"], body)
            audio = read_wav(fields["file"])
            language = (fields.get("language") or b"auto").decode()
            start = time.perf_counter()
            segments, info = self.model.transcribe(audio, language=None if language == "auto" else language, task="transcribe", **self.options)
            text = "".join(s.text for s in segments).strip()
            self.log_message("%.0f ms for %.1f s audio, language=%s", (time.perf_counter() - start) * 1000, len(audio) / 16000, info.language)
            self.reply(200, {"text": text, "language": info.language})
        except Exception as error:  # report failures to the client like whisper-server does
            self.reply(500, {"error": str(error)})

    def reply(self, status, payload):
        data = json.dumps(payload, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--model", default="small")
    p.add_argument("--host", default="127.0.0.1")
    p.add_argument("--port", type=int, default=8083)
    p.add_argument("--compute-type", default="int8")
    p.add_argument("--threads", type=int, default=0, help="CPU threads (0 = library default)")
    p.add_argument("--beam-size", type=int, default=5)
    p.add_argument("--vad", action="store_true", help="enable faster-whisper's Silero VAD filter")
    p.add_argument("--download-root", default="models/faster-whisper")
    a = p.parse_args()
    Handler.model = WhisperModel(a.model, device="cpu", compute_type=a.compute_type, cpu_threads=a.threads, download_root=a.download_root)
    Handler.options = {"beam_size": a.beam_size, "vad_filter": a.vad, "condition_on_previous_text": False}
    print(f"faster-whisper {a.model} ({a.compute_type}, beam {a.beam_size}, vad {a.vad}) on http://{a.host}:{a.port}", flush=True)
    HTTPServer((a.host, a.port), Handler).serve_forever()


if __name__ == "__main__":
    main()
