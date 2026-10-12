package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/akshatsingh/professor-voice-assistant/internal/audio"
	"github.com/akshatsingh/professor-voice-assistant/internal/protocol"
	"github.com/coder/websocket"
)

type result struct {
	Turn           uint32 `json:"turn"`
	Transcript     string `json:"transcript"`
	Language       string `json:"language"`
	Answer         string `json:"answer"`
	AudioSentences int    `json:"audio_sentences"`
	Metrics        any    `json:"metrics"`
}

func main() {
	url := flag.String("url", "ws://127.0.0.1:8090/ws", "assistant WebSocket URL")
	file := flag.String("file", "", "mono PCM16 WAV input")
	language := flag.String("language", "auto", "en, hi, auto, or hinglish")
	followUp := flag.String("follow-up", "", "optional second text turn on the same session")
	flag.Parse()
	if *file == "" {
		fmt.Fprintln(os.Stderr, "-file is required")
		os.Exit(2)
	}
	wav, err := os.ReadFile(*file)
	fatal(err)
	pcm, sampleRate, err := audio.DecodeWAV(wav)
	fatal(err)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, *url, nil)
	fatal(err)
	defer conn.CloseNow()
	conn.SetReadLimit(32 << 20)
	writeJSON(ctx, conn, protocol.Event{Type: "listen_start", TurnID: 1, Language: *language, SampleRate: sampleRate})
	sequence := uint32(0)
	for offset := 0; offset < len(pcm); offset += 4096 {
		end := offset + 4096
		if end > len(pcm) {
			end = len(pcm)
		}
		fatal(conn.Write(ctx, websocket.MessageBinary, protocol.EncodeAudio(protocol.Header{Kind: protocol.AudioInputPCM, TurnID: 1, Sequence: sequence}, pcm[offset:end])))
		sequence++
	}
	writeJSON(ctx, conn, protocol.Event{Type: "listen_stop", TurnID: 1})
	out := result{Turn: 1}
	completed := 0
	for {
		kind, data, readErr := conn.Read(ctx)
		fatal(readErr)
		if kind == websocket.MessageBinary {
			header, _, decodeErr := protocol.DecodeAudio(data)
			fatal(decodeErr)
			if header.Kind == protocol.AudioOutputWAV {
				out.AudioSentences++
			}
			continue
		}
		var event protocol.Event
		fatal(json.Unmarshal(data, &event))
		switch event.Type {
		case "stt_result":
			out.Transcript = event.Text
			out.Language = event.Language
		case "llm_delta":
			out.Answer += event.Text
		case "metrics":
			out.Metrics = event.Metrics
		case "error":
			fatal(fmt.Errorf("%s: %s", event.Code, event.Message))
		case "turn_complete":
			encoded, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(encoded))
			completed++
			if completed == 1 && *followUp != "" {
				out = result{Turn: 2}
				writeJSON(ctx, conn, protocol.Event{Type: "text_input", TurnID: 2, Text: *followUp, Language: *language})
				continue
			}
			return
		}
	}
}

func writeJSON(ctx context.Context, conn *websocket.Conn, event protocol.Event) {
	data, err := json.Marshal(event)
	fatal(err)
	fatal(conn.Write(ctx, websocket.MessageText, data))
}
func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
