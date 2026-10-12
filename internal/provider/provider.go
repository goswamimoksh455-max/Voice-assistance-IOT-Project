package provider

import (
	"context"
	"github.com/akshatsingh/professor-voice-assistant/internal/session"
)

type STTResult struct{ Text, Language string }
type STT interface {
	Transcribe(context.Context, []byte, string) (STTResult, error)
	Ready(context.Context) error
}
type LLM interface {
	Stream(context.Context, []session.Message, string, func(string) error) (string, error)
	Ready(context.Context) error
}
type TTSResult struct {
	WAV        []byte
	SampleRate int
	Voice      string
}
type TTS interface {
	Synthesize(context.Context, string, string) (TTSResult, error)
	Ready(context.Context) error
}
