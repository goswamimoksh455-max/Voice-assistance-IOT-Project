package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/akshatsingh/professor-voice-assistant/internal/audio"
	"github.com/akshatsingh/professor-voice-assistant/internal/eval"
	"github.com/akshatsingh/professor-voice-assistant/internal/provider"
)

// Development-only STT evaluation tools. They are registered only when
// VOICE_DEV_TOOLS=true and answer loopback clients only.

//go:embed devweb/*
var devFS embed.FS

// wavSTT is implemented by providers that can transcribe a WAV file unchanged,
// which the direct baseline needs.
type wavSTT interface {
	TranscribeWAV(context.Context, []byte, string) (provider.STTResult, error)
}

func (s *Server) registerDev(m *http.ServeMux) {
	static, _ := fs.Sub(devFS, "devweb")
	m.Handle("GET /dev/", loopbackOnly(http.StripPrefix("/dev/", http.FileServer(http.FS(static)))))
	m.Handle("GET /dev/api/fixtures", loopbackOnly(http.HandlerFunc(s.devFixtures)))
	m.Handle("GET /dev/fixtures/{id}", loopbackOnly(http.HandlerFunc(s.devFixtureAudio)))
	m.Handle("POST /dev/api/direct", loopbackOnly(http.HandlerFunc(s.devDirect)))
	m.Handle("POST /dev/api/reports", loopbackOnly(http.HandlerFunc(s.devSaveReport)))
}

func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			http.Error(w, "development tools are available on localhost only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) devFixtures(w http.ResponseWriter, _ *http.Request) {
	m, err := eval.LoadManifest(s.cfg.FixturesDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"fixtures": m.Fixtures, "skipped": m.Skipped, "whisper_model": fmt.Sprintf("en: %s · hi/hinglish/auto: %s", s.modelFor("en"), s.modelFor("hi"))})
}

// devFixtureAudio serves only files named in the manifest, never arbitrary paths.
func (s *Server) devFixtureAudio(w http.ResponseWriter, r *http.Request) {
	f, wav, err := s.loadFixture(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", f.ID+".wav"))
	_, _ = w.Write(wav)
}

func (s *Server) loadFixture(id string) (eval.Fixture, []byte, error) {
	m, err := eval.LoadManifest(s.cfg.FixturesDir)
	if err != nil {
		return eval.Fixture{}, nil, err
	}
	f, ok := m.Find(id)
	if !ok || !filepath.IsLocal(f.File) {
		return eval.Fixture{}, nil, errors.New("unknown fixture")
	}
	wav, err := os.ReadFile(filepath.Join(s.cfg.FixturesDir, filepath.FromSlash(f.File)))
	return f, wav, err
}

type directResult struct {
	Fixture  string      `json:"fixture"`
	Text     string      `json:"text"`
	Language string      `json:"language"`
	Sent     string      `json:"language_sent"`
	Model    string      `json:"model"`
	STTMS    int64       `json:"stt_ms"`
	Score    *eval.Score `json:"score,omitempty"`
	Error    string      `json:"error,omitempty"`
}

// devDirect is the direct baseline: the fixture WAV goes to Whisper unchanged,
// bypassing the browser, WebSocket and Go audio processing.
func (s *Server) devDirect(w http.ResponseWriter, r *http.Request) {
	var req struct{ Fixture, Language string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f, wav, err := s.loadFixture(req.Fixture)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	direct, ok := s.stt.(wavSTT)
	if !ok {
		http.Error(w, "STT provider cannot transcribe WAV files", http.StatusNotImplemented)
		return
	}
	language := req.Language
	if language == "" {
		language = f.Language
	}
	out := directResult{Fixture: f.ID, Sent: sttLanguage(language), Model: s.modelFor(sttLanguage(language))}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.STTTimeout)
	defer cancel()
	start := time.Now()
	res, err := direct.TranscribeWAV(ctx, wav, out.Sent)
	out.STTMS = time.Since(start).Milliseconds()
	if err != nil {
		out.Error = err.Error()
	} else {
		score := eval.Compare(f.Reference, res.Text, f.Language)
		out.Text, out.Language, out.Score = res.Text, res.Language, &score
	}
	writeJSON(w, out)
}

var reportLabel = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func (s *Server) devSaveReport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil || !json.Valid(body) {
		http.Error(w, "report must be JSON", http.StatusBadRequest)
		return
	}
	if err = os.MkdirAll(s.cfg.ReportDir, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	label := reportLabel.ReplaceAllString(r.URL.Query().Get("label"), "-")
	if label == "" {
		label = "report"
	}
	name := time.Now().Format("20060102-150405") + "-" + label + ".json"
	if err = os.WriteFile(filepath.Join(s.cfg.ReportDir, name), body, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"path": filepath.Join(s.cfg.ReportDir, name)})
}

// fixtureEval scores a WebSocket turn against its fixture and measures how
// faithfully the audio Go received (after resampling) matches the recording.
func (s *Server) fixtureEval(r recording, whisperPCM []byte, transcript string, resampleMS, whisperMS int64) map[string]any {
	out := map[string]any{
		"fixture": r.fixture, "input_sample_rate": r.sampleRate, "input_samples": len(r.pcm) / 2,
		"input_duration_ms": int64(len(r.pcm)/2) * 1000 / int64(r.sampleRate), "whisper_sample_rate": audio.WhisperSampleRate,
		"whisper_samples": len(whisperPCM) / 2, "whisper_duration_ms": int64(len(whisperPCM)/2) * 1000 / audio.WhisperSampleRate,
		"language_sent": sttLanguage(r.language), "model": s.modelFor(sttLanguage(r.language)), "resample_ms": resampleMS, "whisper_ms": whisperMS,
		"levels": audio.AnalyzePCM16(r.pcm, r.sampleRate),
	}
	f, wav, err := s.loadFixture(r.fixture)
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	out["score"] = eval.Compare(f.Reference, transcript, f.Language)
	if ref, rate, _, err := audio.DecodeToPCM16(wav); err == nil && rate == audio.WhisperSampleRate {
		out["fidelity"] = audio.Align(ref, whisperPCM, rate, rate/2)
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
