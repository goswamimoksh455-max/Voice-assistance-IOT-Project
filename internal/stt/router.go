package stt

import (
	"context"
	"fmt"

	"github.com/akshatsingh/professor-voice-assistant/internal/provider"
)

// Router sends English to a fast model and every other language (hi, and auto
// detection) to a more accurate multilingual model. Measured on FLEURS and
// OpenSLR 104 (docs/STT_EVALUATION.md): small is accurate and fast for English,
// while Hindi/Hinglish need large-v3-turbo.
type Router struct {
	English, Other *Whisper
	EnglishModel   string
	OtherModel     string
}

func (r *Router) pick(language string) *Whisper {
	if language == "en" || r.Other == nil {
		return r.English
	}
	return r.Other
}

// Model names the model that will transcribe the given Whisper language.
func (r *Router) Model(language string) string {
	if language == "en" || r.Other == nil {
		return r.EnglishModel
	}
	return r.OtherModel
}

func (r *Router) Transcribe(ctx context.Context, pcm []byte, language string) (provider.STTResult, error) {
	return r.pick(language).Transcribe(ctx, pcm, language)
}

func (r *Router) TranscribeWAV(ctx context.Context, wav []byte, language string) (provider.STTResult, error) {
	return r.pick(language).TranscribeWAV(ctx, wav, language)
}

func (r *Router) Ready(ctx context.Context) error {
	if err := r.English.Ready(ctx); err != nil {
		return fmt.Errorf("english model: %w", err)
	}
	if r.Other != nil {
		if err := r.Other.Ready(ctx); err != nil {
			return fmt.Errorf("multilingual model: %w", err)
		}
	}
	return nil
}
