package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/akshatsingh/professor-voice-assistant/internal/audio"
	"github.com/akshatsingh/professor-voice-assistant/internal/config"
	"github.com/akshatsingh/professor-voice-assistant/internal/metrics"
	"github.com/akshatsingh/professor-voice-assistant/internal/pipeline"
	"github.com/akshatsingh/professor-voice-assistant/internal/protocol"
	"github.com/akshatsingh/professor-voice-assistant/internal/provider"
	"github.com/akshatsingh/professor-voice-assistant/internal/session"
	"github.com/coder/websocket"
)

//go:embed web/*
var webFS embed.FS

type Server struct {
	cfg     config.Config
	log     *slog.Logger
	stt     provider.STT
	llm     provider.LLM
	tts     provider.TTS
	clients atomic.Int64
}

func New(c config.Config, l *slog.Logger, s provider.STT, m provider.LLM, t provider.TTS) *Server {
	return &Server{c, l, s, m, t, atomic.Int64{}}
}
func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.Handle("/", http.FileServer(http.FS(webFS)))
	m.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "clients": s.clients.Load()})
	})
	m.HandleFunc("/readyz", s.ready)
	m.HandleFunc("/ws", s.ws)
	if s.cfg.DevTools {
		s.registerDev(m)
	}
	return secure(m)
}
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/" {
			r.URL.Path = "/web/"
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	checks := map[string]string{}
	for name, fn := range map[string]func(context.Context) error{"whisper": s.stt.Ready, "gemini": s.llm.Ready, "piper": s.tts.Ready} {
		if e := fn(ctx); e != nil {
			checks[name] = e.Error()
		} else {
			checks[name] = "ok"
		}
	}
	status := http.StatusOK
	if checks["whisper"] != "ok" || checks["piper"] != "ok" {
		status = http.StatusServiceUnavailable
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(checks)
}

type client struct {
	s       *Server
	c       *websocket.Conn
	writeMu sync.Mutex
	sess    *session.Session
	recMu   sync.Mutex
	rec     recording
	// dropped is the turn whose recording was discarded after an audio error;
	// its remaining in-flight frames and listen_stop are ignored silently.
	dropped uint32
	debug   bool
}
type recording struct {
	active     bool
	turn       uint32
	language   string
	sampleRate int
	nextSeq    uint32
	pcm        []byte
	debug      bool
	sttOnly    bool   // development tools: stop after transcription
	fixture    string // development tools: score against this fixture
}

func (cl *client) send(ctx context.Context, e protocol.Event) error {
	b, err := json.Marshal(e)
	if err != nil { // never send an empty frame the client cannot parse
		cl.s.log.Error("event encoding failed", "type", e.Type, "turn_id", e.TurnID, "error", err)
		b, _ = json.Marshal(protocol.Event{Type: "error", TurnID: e.TurnID, Code: "internal", Message: "event encoding failed"})
	}
	cl.writeMu.Lock()
	defer cl.writeMu.Unlock()
	return cl.c.Write(ctx, websocket.MessageText, b)
}
func (cl *client) audio(ctx context.Context, h protocol.Header, b []byte) error {
	cl.writeMu.Lock()
	defer cl.writeMu.Unlock()
	return cl.c.Write(ctx, websocket.MessageBinary, protocol.EncodeAudio(h, b))
}
func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	c, e := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"localhost:*", "127.0.0.1:*"}})
	if e != nil {
		return
	}
	defer c.CloseNow()
	s.clients.Add(1)
	defer s.clients.Add(-1)
	cl := &client{s: s, c: c, sess: session.New(s.cfg.HistoryTurns)}
	ctx := r.Context()
	_ = cl.send(ctx, protocol.Event{Type: "hello", Version: protocol.Version, Debug: s.cfg.AudioDebug, Audio: map[string]any{"input": "pcm_s16le", "sample_rate": "client_actual", "whisper_sample_rate": audio.WhisperSampleRate, "channels": 1, "binary_header_bytes": protocol.HeaderSize, "output": "wav", "whisper_model": s.modelFor("en"), "whisper_multilingual_model": s.modelFor("hi")}})
	_ = cl.send(ctx, protocol.Event{Type: "ready"})
	for {
		kind, b, e := c.Read(ctx)
		if e != nil {
			cl.sess.Abort()
			return
		}
		if kind == websocket.MessageText {
			if e = cl.control(ctx, b); e != nil {
				cl.fail(ctx, 0, "bad_request", e)
			}
		} else if kind == websocket.MessageBinary {
			if turn, e := cl.binary(b); e != nil {
				cl.fail(ctx, turn, "bad_audio", e)
			}
		}
	}
}
func (cl *client) control(ctx context.Context, b []byte) error {
	e, err := protocol.DecodeEvent(b)
	if err != nil {
		return err
	}
	switch e.Type {
	case "listen_start":
		if e.SampleRate < 8000 || e.SampleRate > 192000 {
			return errors.New("unsupported microphone sample rate")
		}
		if (e.STTOnly || e.Fixture != "") && !cl.s.cfg.DevTools {
			return errors.New("stt_only and fixture require VOICE_DEV_TOOLS=true")
		}
		cl.sess.Abort()
		cl.recMu.Lock()
		cl.rec = recording{active: true, turn: e.TurnID, language: e.Language, sampleRate: e.SampleRate, debug: cl.debug && cl.s.cfg.AudioDebugAllowed(), sttOnly: e.STTOnly, fixture: e.Fixture}
		cl.dropped = 0
		cl.recMu.Unlock()
		return cl.send(ctx, protocol.Event{Type: "ready", TurnID: e.TurnID})
	case "listen_stop":
		cl.recMu.Lock()
		r := cl.rec
		cl.rec = recording{}
		dropped := e.TurnID != 0 && e.TurnID == cl.dropped
		cl.recMu.Unlock()
		if dropped {
			return nil
		}
		if !r.active || r.turn != e.TurnID {
			return errors.New("no matching recording")
		}
		if err = audio.ValidatePCM16(r.pcm, r.sampleRate, cl.s.cfg.MaxRecordingSeconds); err != nil {
			cl.fail(ctx, r.turn, "bad_request", err)
			return nil
		}
		go cl.runTurn(ctx, r)
		return nil
	case "abort":
		cl.sess.Abort()
		return cl.send(ctx, protocol.Event{Type: "turn_complete", TurnID: e.TurnID, Message: "aborted"})
	case "clear":
		cl.sess.Abort()
		cl.sess.Clear()
		return cl.send(ctx, protocol.Event{Type: "ready", Message: "conversation cleared"})
	case "text_input":
		if e.Text == "" {
			return errors.New("empty text")
		}
		go cl.runText(ctx, e.TurnID, e.Text, e.Language)
		return nil
	case "playback_started":
		cl.s.log.Info("client playback started", "turn_id", e.TurnID, "client_metrics", e.Metrics)
		return nil
	case "debug_enable":
		cl.recMu.Lock()
		cl.debug = e.Debug && cl.s.cfg.AudioDebugAllowed()
		enabled := cl.debug
		cl.recMu.Unlock()
		return cl.send(ctx, protocol.Event{Type: "debug_state", Debug: enabled})
	default:
		return fmt.Errorf("unknown event %q", e.Type)
	}
}

// binary appends one PCM frame to the active recording. The first invalid frame
// discards the recording and is reported once; later frames of that turn are
// dropped so a single fault cannot produce an error per frame.
func (cl *client) binary(b []byte) (uint32, error) {
	h, p, e := protocol.DecodeAudio(b)
	if e != nil {
		return 0, e
	}
	if h.Kind != protocol.AudioInputPCM {
		return h.TurnID, errors.New("unexpected audio kind")
	}
	cl.recMu.Lock()
	defer cl.recMu.Unlock()
	if h.TurnID != 0 && h.TurnID == cl.dropped {
		return 0, nil
	}
	switch {
	case !cl.rec.active || h.TurnID != cl.rec.turn || h.Sequence != cl.rec.nextSeq:
		e = errors.New("stale or out-of-order audio frame")
	case len(cl.rec.pcm)+len(p) > cl.rec.sampleRate*cl.s.cfg.MaxRecordingSeconds*2:
		e = errors.New("recording duration exceeded")
	default:
		cl.rec.nextSeq++
		cl.rec.pcm = append(cl.rec.pcm, p...)
		return h.TurnID, nil
	}
	if cl.rec.active && h.TurnID == cl.rec.turn {
		cl.rec = recording{}
	}
	cl.dropped = h.TurnID
	return h.TurnID, e
}
func (cl *client) runTurn(parent context.Context, r recording) {
	ctx := cl.sess.Begin(parent)
	started := time.Now()
	whisperPCM, e := audio.PrepareWhisper(r.pcm, r.sampleRate, cl.s.cfg.MaxRecordingSeconds)
	if e != nil {
		cl.fail(ctx, r.turn, "audio_failed", e)
		return
	}
	if r.debug {
		inputStats := audio.AnalyzePCM16(r.pcm, r.sampleRate)
		outputStats := audio.AnalyzePCM16(whisperPCM, audio.WhisperSampleRate)
		_ = cl.send(ctx, protocol.Event{Type: "audio_debug", TurnID: r.turn, Language: sttLanguage(r.language), Audio: map[string]any{"input": inputStats, "whisper": outputStats, "model": cl.s.modelFor(sttLanguage(r.language))}})
		_ = cl.audio(ctx, protocol.Header{Kind: protocol.AudioDebugInputWAV, TurnID: r.turn}, audio.WAV(r.pcm, r.sampleRate))
		_ = cl.audio(ctx, protocol.Header{Kind: protocol.AudioDebugWhisperWAV, TurnID: r.turn}, audio.WAV(whisperPCM, audio.WhisperSampleRate))
	}
	sttCtx, cancel := context.WithTimeout(ctx, cl.s.cfg.STTTimeout)
	whisperStart := time.Now()
	res, e := cl.s.stt.Transcribe(sttCtx, whisperPCM, sttLanguage(r.language))
	cancel()
	if e != nil {
		cl.fail(ctx, r.turn, "stt_failed", e)
		return
	}
	sttDone := time.Now()
	if r.sttOnly {
		cl.finishSTTOnly(ctx, r, whisperPCM, res, whisperStart.Sub(started), sttDone.Sub(whisperStart))
		return
	}
	if r.debug {
		_ = cl.send(ctx, protocol.Event{Type: "audio_debug_transcript", TurnID: r.turn, Text: res.Text, Language: res.Language})
	}
	result := protocol.Event{Type: "stt_result", TurnID: r.turn, Text: res.Text, Language: res.Language}
	if r.fixture != "" { // development full-pipeline smoke test: score the transcript too
		result.Eval = cl.s.fixtureEval(r, whisperPCM, res.Text, whisperStart.Sub(started).Milliseconds(), sttDone.Sub(whisperStart).Milliseconds())
	}
	_ = cl.send(ctx, result)
	cl.generate(ctx, r.turn, res.Text, normalizedLanguage(r.language, res.Language), started, sttDone)
}

// sttLanguage maps the UI selection to a Whisper language. Hinglish is decoded
// as Hindi: measured with real recordings, "en" rejects Hindi-heavy speech and
// "auto" is slower without being more accurate.
// modelFor names the Whisper model that transcribes the given Whisper language.
func (s *Server) modelFor(language string) string {
	if m, ok := s.stt.(interface{ Model(string) string }); ok {
		return m.Model(language)
	}
	return s.cfg.WhisperModel
}

// finishSTTOnly ends a development STT-only turn without calling Gemini or Piper.
func (cl *client) finishSTTOnly(ctx context.Context, r recording, whisperPCM []byte, res provider.STTResult, resample, whisper time.Duration) {
	e := protocol.Event{Type: "stt_result", TurnID: r.turn, Text: res.Text, Language: res.Language}
	if r.fixture != "" {
		e.Eval = cl.s.fixtureEval(r, whisperPCM, res.Text, resample.Milliseconds(), whisper.Milliseconds())
	}
	_ = cl.send(ctx, e)
	_ = cl.send(ctx, protocol.Event{Type: "metrics", TurnID: r.turn, Metrics: metrics.Turn{STTMS: (resample + whisper).Milliseconds(), Inference: "stt_only"}})
	_ = cl.send(ctx, protocol.Event{Type: "turn_complete", TurnID: r.turn, Message: "stt_only"})
}

func sttLanguage(language string) string {
	switch language {
	case "en", "hi":
		return language
	case "hinglish":
		return "hi"
	}
	return "auto"
}
func (cl *client) runText(parent context.Context, turn uint32, text, lang string) {
	ctx := cl.sess.Begin(parent)
	now := time.Now()
	_ = cl.send(ctx, protocol.Event{Type: "stt_result", TurnID: turn, Text: text, Language: lang})
	cl.generate(ctx, turn, text, normalizedLanguage(lang, lang), now, now)
}
func normalizedLanguage(selected, detected string) string {
	if selected != "" && selected != "auto" {
		return selected
	}
	if detected == "hi" || detected == "en" {
		return detected
	}
	return "auto"
}
func (cl *client) generate(ctx context.Context, turn uint32, prompt, lang string, started, sttDone time.Time) {
	workCtx, workCancel := context.WithCancel(ctx)
	defer workCancel()
	llmCtx, cancel := context.WithTimeout(workCtx, cl.s.cfg.LLMTimeout)
	defer cancel()
	m := metrics.Turn{STTMS: sttDone.Sub(started).Milliseconds(), Inference: "warm"}
	sentences := make(chan string, 4)
	ttsErr := make(chan error, 1)
	var seq uint32
	var firstTTS time.Duration
	go func() {
		for sentence := range sentences {
			t0 := time.Now()
			tctx, tcancel := context.WithTimeout(workCtx, cl.s.cfg.TTSTimeout)
			a, e := cl.s.tts.Synthesize(tctx, sentence, lang)
			tcancel()
			if e != nil {
				workCancel()
				ttsErr <- e
				return
			}
			if seq == 0 {
				firstTTS = time.Since(t0)
				m.FirstTTSMS = firstTTS.Milliseconds()
			}
			_ = cl.send(workCtx, protocol.Event{Type: "tts_start", TurnID: turn, Sequence: seq, Text: sentence, Audio: map[string]any{"format": "wav", "sample_rate": a.SampleRate, "voice": a.Voice}})
			if e = cl.audio(workCtx, protocol.Header{Kind: protocol.AudioOutputWAV, TurnID: turn, Sequence: seq}, a.WAV); e != nil {
				workCancel()
				ttsErr <- e
				return
			}
			if seq == 0 {
				m.FirstAudioMS = time.Since(started).Milliseconds()
			}
			_ = cl.send(workCtx, protocol.Event{Type: "tts_end", TurnID: turn, Sequence: seq})
			seq++
		}
		ttsErr <- nil
	}()
	split := pipeline.Splitter{}
	firstText := time.Duration(0)
	llmStart := time.Now()
	answer, e := cl.s.llm.Stream(llmCtx, cl.sess.History(), prompt, func(delta string) error {
		if firstText == 0 {
			firstText = time.Since(llmStart)
			m.LLMFirstTextMS = firstText.Milliseconds()
		}
		if err := cl.send(workCtx, protocol.Event{Type: "llm_delta", TurnID: turn, Text: delta}); err != nil {
			return err
		}
		for _, v := range split.Push(delta) {
			select {
			case sentences <- v:
			case <-workCtx.Done():
				return workCtx.Err()
			}
		}
		return nil
	})
	m.LLMTotalMS = time.Since(llmStart).Milliseconds()
	if e != nil {
		workCancel()
	} else if tail := split.Flush(); tail != "" {
		select {
		case sentences <- tail:
		case <-workCtx.Done():
		}
	}
	close(sentences)
	te := <-ttsErr
	if e != nil {
		cl.fail(ctx, turn, "llm_failed", e)
		return
	}
	if te != nil {
		cl.fail(ctx, turn, "tts_failed", te)
		return
	}
	cl.sess.Add("user", prompt)
	cl.sess.Add("assistant", answer)
	m.TotalMS = time.Since(started).Milliseconds()
	_ = cl.send(ctx, protocol.Event{Type: "metrics", TurnID: turn, Metrics: m})
	_ = cl.send(ctx, protocol.Event{Type: "turn_complete", TurnID: turn})
	cl.s.log.Info("turn complete", "turn_id", turn, "stt_ms", m.STTMS, "llm_first_text_ms", m.LLMFirstTextMS, "llm_total_ms", m.LLMTotalMS, "first_tts_ms", m.FirstTTSMS, "first_audio_ms", m.FirstAudioMS, "total_ms", m.TotalMS)
}
func (cl *client) fail(ctx context.Context, turn uint32, code string, e error) {
	if errors.Is(e, context.Canceled) {
		return
	}
	cl.s.log.Warn("turn failed", "turn_id", turn, "code", code, "error", e)
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = cl.send(sendCtx, protocol.Event{Type: "error", TurnID: turn, Code: code, Message: e.Error()})
}
