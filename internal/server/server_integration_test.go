package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akshatsingh/professor-voice-assistant/internal/audio"
	"github.com/akshatsingh/professor-voice-assistant/internal/config"
	"github.com/akshatsingh/professor-voice-assistant/internal/eval"
	"github.com/akshatsingh/professor-voice-assistant/internal/protocol"
	"github.com/akshatsingh/professor-voice-assistant/internal/provider"
	"github.com/akshatsingh/professor-voice-assistant/internal/session"
	"github.com/coder/websocket"
)

type capturedSTT struct{ calls chan sttCall }
type sttCall struct {
	bytes    int
	language string
}

func (s *capturedSTT) Ready(context.Context) error { return nil }
func (s *capturedSTT) Transcribe(_ context.Context, pcm []byte, language string) (provider.STTResult, error) {
	s.calls <- sttCall{len(pcm), language}
	return provider.STTResult{Text: "test transcript", Language: "en"}, nil
}

type fakeLLM struct{}

func (*fakeLLM) Ready(context.Context) error { return nil }
func (*fakeLLM) Stream(_ context.Context, _ []session.Message, prompt string, delta func(string) error) (string, error) {
	answer := "Received " + prompt + "."
	return answer, delta(answer)
}

type fakeTTS struct{}

func (*fakeTTS) Ready(context.Context) error { return nil }
func (*fakeTTS) Synthesize(context.Context, string, string) (provider.TTSResult, error) {
	return provider.TTSResult{WAV: audio.WAV(make([]byte, 3200), 16000), SampleRate: 16000, Voice: "fake"}, nil
}

func TestWebSocketResamplesOrdersAndFlushesRecording(t *testing.T) {
	if os.Getenv("VOICE_NETWORK_TESTS") != "1" {
		t.Skip("set VOICE_NETWORK_TESTS=1 to permit localhost listener")
	}
	stt := &capturedSTT{calls: make(chan sttCall, 1)}
	cfg := config.Config{MaxRecordingSeconds: 30, HistoryTurns: 2, STTTimeout: time.Second, LLMTimeout: time.Second, TTSTimeout: time.Second, AudioDebug: true, WhisperModel: "ggml-base.bin"}
	app := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), stt, &fakeLLM{}, &fakeTTS{})
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(8 << 20)
	readTextType(t, ctx, conn, "hello")
	readTextType(t, ctx, conn, "ready")
	writeEvent(t, ctx, conn, protocol.Event{Type: "debug_enable", Debug: true})
	readTextType(t, ctx, conn, "debug_state")
	writeEvent(t, ctx, conn, protocol.Event{Type: "listen_start", TurnID: 7, Language: "hinglish", SampleRate: 48000})
	readTextType(t, ctx, conn, "ready")
	pcm := audio.SpeechLikePCM(48000, 1, 0.3)
	sequence := uint32(0)
	for offset := 0; offset < len(pcm); offset += 2048 {
		end := offset + 2048
		if end > len(pcm) {
			end = len(pcm)
		}
		payload := protocol.EncodeAudio(protocol.Header{Kind: protocol.AudioInputPCM, TurnID: 7, Sequence: sequence}, pcm[offset:end])
		if err = conn.Write(ctx, websocket.MessageBinary, payload); err != nil {
			t.Fatal(err)
		}
		sequence++
	}
	writeEvent(t, ctx, conn, protocol.Event{Type: "listen_stop", TurnID: 7})
	call := awaitSTT(t, stt)
	if call.bytes != audio.WhisperSampleRate*2 {
		t.Fatalf("STT got %d bytes, want %d", call.bytes, audio.WhisperSampleRate*2)
	}
	if call.language != "hi" {
		t.Fatalf("Hinglish language = %q", call.language)
	}
	seenInput, seenWhisper, seenTranscript := false, false, false
	for !seenTranscript {
		kind, data, readErr := conn.Read(ctx)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if kind == websocket.MessageBinary {
			header, _, decodeErr := protocol.DecodeAudio(data)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			seenInput = seenInput || header.Kind == protocol.AudioDebugInputWAV
			seenWhisper = seenWhisper || header.Kind == protocol.AudioDebugWhisperWAV
			continue
		}
		var event protocol.Event
		if json.Unmarshal(data, &event) != nil {
			continue
		}
		seenTranscript = event.Type == "stt_result"
	}
	if !seenInput || !seenWhisper {
		t.Fatalf("missing debug audio input=%v whisper=%v", seenInput, seenWhisper)
	}
}

func TestBadFrameDiscardsRecordingAndReportsOnce(t *testing.T) {
	if os.Getenv("VOICE_NETWORK_TESTS") != "1" {
		t.Skip("set VOICE_NETWORK_TESTS=1 to permit localhost listener")
	}
	stt := &capturedSTT{calls: make(chan sttCall, 1)}
	cfg := config.Config{MaxRecordingSeconds: 30, HistoryTurns: 2, STTTimeout: time.Second, LLMTimeout: time.Second, TTSTimeout: time.Second}
	app := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), stt, &fakeLLM{}, &fakeTTS{})
	httpServer := httptest.NewServer(app.Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	readTextType(t, ctx, conn, "hello")
	readTextType(t, ctx, conn, "ready")
	speech := audio.SpeechLikePCM(48000, 1, 0.3)
	const frameBytes = 960
	send := func(turn, sequence uint32) {
		t.Helper()
		frame := speech[int(sequence)*frameBytes : int(sequence+1)*frameBytes]
		if err := conn.Write(ctx, websocket.MessageBinary, protocol.EncodeAudio(protocol.Header{Kind: protocol.AudioInputPCM, TurnID: turn, Sequence: sequence}, frame)); err != nil {
			t.Fatal(err)
		}
	}

	writeEvent(t, ctx, conn, protocol.Event{Type: "listen_start", TurnID: 1, Language: "en", SampleRate: 48000})
	readTextType(t, ctx, conn, "ready")
	send(1, 0)
	for sequence := uint32(2); sequence < 50; sequence++ { // sequence 1 is missing
		send(1, sequence)
	}
	writeEvent(t, ctx, conn, protocol.Event{Type: "listen_stop", TurnID: 1})
	writeEvent(t, ctx, conn, protocol.Event{Type: "debug_enable"}) // sentinel: answered in order
	var errors []protocol.Event
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var event protocol.Event
		if json.Unmarshal(data, &event) != nil {
			continue
		}
		if event.Type == "error" {
			errors = append(errors, event)
		}
		if event.Type == "debug_state" {
			break
		}
	}
	if len(errors) != 1 || errors[0].TurnID != 1 || errors[0].Code != "bad_audio" {
		t.Fatalf("want one bad_audio error for turn 1, got %+v", errors)
	}
	select {
	case call := <-stt.calls:
		t.Fatalf("discarded recording reached STT (%d bytes)", call.bytes)
	default:
	}

	// The next turn records normally.
	writeEvent(t, ctx, conn, protocol.Event{Type: "listen_start", TurnID: 2, Language: "en", SampleRate: 48000})
	readTextType(t, ctx, conn, "ready")
	for sequence := uint32(0); sequence < 50; sequence++ {
		send(2, sequence)
	}
	writeEvent(t, ctx, conn, protocol.Event{Type: "listen_stop", TurnID: 2})
	if call := awaitSTT(t, stt); call.bytes != 50*frameBytes/3 {
		t.Fatalf("turn 2 STT got %d bytes, want %d", call.bytes, 50*frameBytes/3)
	}
}

func awaitSTT(t *testing.T, stt *capturedSTT) sttCall {
	t.Helper()
	select {
	case call := <-stt.calls:
		return call
	case <-time.After(5 * time.Second):
		t.Fatal("recording never reached STT")
		return sttCall{}
	}
}

func writeEvent(t *testing.T, ctx context.Context, conn *websocket.Conn, event protocol.Event) {
	t.Helper()
	data, _ := json.Marshal(event)
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
}
func readTextType(t *testing.T, ctx context.Context, conn *websocket.Conn, want string) {
	t.Helper()
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.MessageText {
			continue
		}
		var event protocol.Event
		if err = json.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != want {
			t.Fatalf("got event %q want %q", event.Type, want)
		}
		return
	}
}

type countingLLM struct{ calls atomic.Int32 }

func (*countingLLM) Ready(context.Context) error { return nil }
func (l *countingLLM) Stream(context.Context, []session.Message, string, func(string) error) (string, error) {
	l.calls.Add(1)
	return "", nil
}

func TestSTTOnlyFixtureTurnScoresWithoutLLM(t *testing.T) {
	if os.Getenv("VOICE_NETWORK_TESTS") != "1" {
		t.Skip("set VOICE_NETWORK_TESTS=1 to permit localhost listener")
	}
	dir := t.TempDir()
	fixturePCM := audio.SpeechLikePCM(16000, 1, 0.3)
	if err := os.WriteFile(filepath.Join(dir, "f.wav"), audio.WAV(fixturePCM, 16000), 0o644); err != nil {
		t.Fatal(err)
	}
	m := eval.Manifest{Version: 1, Fixtures: []eval.Fixture{{ID: "fx", Language: "en", File: "f.wav", Reference: "test transcript", SampleRate: 16000}}}
	if err := m.Save(dir); err != nil {
		t.Fatal(err)
	}
	for _, devTools := range []bool{false, true} {
		stt, llm := &capturedSTT{calls: make(chan sttCall, 1)}, &countingLLM{}
		cfg := config.Config{MaxRecordingSeconds: 30, HistoryTurns: 2, STTTimeout: time.Second, LLMTimeout: time.Second, TTSTimeout: time.Second, DevTools: devTools, FixturesDir: dir}
		httpServer := httptest.NewServer(New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), stt, llm, &fakeTTS{}).Handler())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", nil)
		if err != nil {
			t.Fatal(err)
		}
		readTextType(t, ctx, conn, "hello")
		readTextType(t, ctx, conn, "ready")
		writeEvent(t, ctx, conn, protocol.Event{Type: "listen_start", TurnID: 1, Language: "en", SampleRate: 48000, STTOnly: true, Fixture: "fx"})
		if !devTools {
			if e := readEvent(t, ctx, conn); e.Type != "error" {
				t.Fatalf("stt_only accepted without VOICE_DEV_TOOLS: %+v", e)
			}
		} else {
			readTextType(t, ctx, conn, "ready")
			pcm48, _ := audio.ResamplePCM16(fixturePCM, 16000, 48000)
			for i, offset := 0, 0; offset < len(pcm48); i, offset = i+1, offset+256 {
				frame := pcm48[offset:min(offset+256, len(pcm48))]
				if err = conn.Write(ctx, websocket.MessageBinary, protocol.EncodeAudio(protocol.Header{Kind: protocol.AudioInputPCM, TurnID: 1, Sequence: uint32(i)}, frame)); err != nil {
					t.Fatal(err)
				}
			}
			writeEvent(t, ctx, conn, protocol.Event{Type: "listen_stop", TurnID: 1})
			var result protocol.Event
			for result.Type != "turn_complete" {
				e := readEvent(t, ctx, conn)
				if e.Type == "error" {
					t.Fatalf("turn failed: %+v", e)
				}
				if e.Type == "stt_result" {
					result = e
					var ev struct {
						Score    eval.Score     `json:"score"`
						Fidelity audio.Fidelity `json:"fidelity"`
						Samples  int            `json:"whisper_samples"`
					}
					b, _ := json.Marshal(e.Eval)
					_ = json.Unmarshal(b, &ev)
					if !ev.Score.Exact || ev.Fidelity.Correlation < 0.99 || ev.Samples != len(fixturePCM)/2 {
						t.Fatalf("eval = %s", b)
					}
				}
				if e.Type == "turn_complete" {
					result.Type = e.Type
				}
			}
			if n := llm.calls.Load(); n != 0 {
				t.Fatalf("STT-only turn called the LLM %d times", n)
			}
		}
		conn.CloseNow()
		cancel()
		httpServer.Close()
	}
}

func readEvent(t *testing.T, ctx context.Context, conn *websocket.Conn) protocol.Event {
	t.Helper()
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.MessageText {
			continue
		}
		var e protocol.Event
		if err = json.Unmarshal(data, &e); err != nil {
			t.Fatal(err)
		}
		return e
	}
}
