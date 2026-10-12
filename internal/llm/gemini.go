package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/akshatsingh/professor-voice-assistant/internal/session"
	"google.golang.org/genai"
)

const systemPrompt = `You are a voice assistant for a college professor. Reply in the user's English, Hindi, or Hinglish. Give a short, natural spoken answer, normally 1-3 sentences. Keep technical terms accurate. Avoid markdown and long lists. Never invent schedules, student records, or private information; clearly say when you cannot access something. Preserve conversational context.`

type Gemini struct {
	Client          *genai.Client
	Model, Thinking string
	MaxTokens       int32
}

func New(ctx context.Context, key, model, thinking string, max int) (*Gemini, error) {
	if key == "" {
		return nil, errors.New("GEMINI_API_KEY is not set")
	}
	c, e := genai.NewClient(ctx, &genai.ClientConfig{APIKey: key, Backend: genai.BackendGeminiAPI})
	if e != nil {
		return nil, e
	}
	return &Gemini{c, model, thinking, int32(max)}, nil
}
func (g *Gemini) Ready(context.Context) error { return nil }
func (g *Gemini) Stream(ctx context.Context, h []session.Message, prompt string, onDelta func(string) error) (string, error) {
	contents := make([]*genai.Content, 0, len(h)+1)
	for _, m := range h {
		var role genai.Role = genai.RoleUser
		if m.Role == "assistant" {
			role = genai.RoleModel
		}
		contents = append(contents, genai.NewContentFromText(m.Text, role))
	}
	contents = append(contents, genai.NewContentFromText(prompt, genai.RoleUser))
	levels := map[string]genai.ThinkingLevel{"minimal": genai.ThinkingLevelMinimal, "low": genai.ThinkingLevelLow, "medium": genai.ThinkingLevelMedium, "high": genai.ThinkingLevelHigh}
	level := levels[g.Thinking]
	cfg := &genai.GenerateContentConfig{SystemInstruction: genai.NewContentFromText(systemPrompt, genai.RoleUser), MaxOutputTokens: g.MaxTokens, ThinkingConfig: &genai.ThinkingConfig{ThinkingLevel: level}}
	var full strings.Builder
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		full.Reset()
		last = nil
		emitted := false
		for resp, e := range g.Client.Models.GenerateContentStream(ctx, g.Model, contents, cfg) {
			if e != nil {
				last = e
				break
			}
			for _, delta := range responseText(resp) {
				full.WriteString(delta)
				emitted = true
				if e = onDelta(delta); e != nil {
					return "", e
				}
			}
		}
		if last == nil && strings.TrimSpace(full.String()) != "" {
			return strings.TrimSpace(full.String()), nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if attempt == 0 && !emitted && retryable(last) {
			select {
			case <-time.After(250 * time.Millisecond):
			case <-ctx.Done():
				return "", ctx.Err()
			}
			continue
		}
		break
	}
	if last == nil {
		last = errors.New("Gemini returned an empty response")
	}
	return "", fmt.Errorf("Gemini request failed: %w", last)
}

func responseText(resp *genai.GenerateContentResponse) []string {
	var out []string
	for _, c := range resp.Candidates {
		if c.Content == nil {
			continue
		}
		for _, p := range c.Content.Parts {
			if p.Text != "" && !p.Thought {
				out = append(out, p.Text)
			}
		}
	}
	return out
}
func retryable(e error) bool {
	if e == nil {
		return false
	}
	s := strings.ToLower(e.Error())
	return strings.Contains(s, "429") || strings.Contains(s, "rate") || strings.Contains(s, "503") || strings.Contains(s, "unavailable")
}
