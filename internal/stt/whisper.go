package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"

	"github.com/akshatsingh/professor-voice-assistant/internal/audio"
	"github.com/akshatsingh/professor-voice-assistant/internal/provider"
)

type Whisper struct {
	BaseURL string
	Client  *http.Client
	// Options are extra whisper-server form fields (decoding parameters such
	// as beam_size or temperature_inc) sent with every request.
	Options map[string]string
}

func New(url string, client *http.Client) *Whisper {
	return &Whisper{BaseURL: strings.TrimRight(url, "/"), Client: client}
}

// ParseOptions reads "key=value,key=value" decoding options.
func ParseOptions(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		if pair = strings.TrimSpace(pair); pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("invalid Whisper option %q", pair)
		}
		k = strings.TrimSpace(k)
		switch k {
		case "file", "language", "translate", "response_format":
			return nil, fmt.Errorf("Whisper option %q is managed by the application", k)
		}
		out[k] = strings.TrimSpace(v)
	}
	return out, nil
}
func (w *Whisper) Ready(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, w.BaseURL+"/", nil)
	r, e := w.Client.Do(req)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	if r.StatusCode >= 500 {
		return fmt.Errorf("whisper unavailable: %s", r.Status)
	}
	return nil
}
func (w *Whisper) Transcribe(ctx context.Context, pcm []byte, language string) (provider.STTResult, error) {
	return w.TranscribeWAV(ctx, audio.WAV(pcm, audio.WhisperSampleRate), language)
}

// TranscribeWAV sends an existing WAV file unchanged, with exactly the request
// parameters used for live audio. Evaluation uses it as the direct baseline.
func (w *Whisper) TranscribeWAV(ctx context.Context, wav []byte, language string) (provider.STTResult, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	f, _ := mw.CreateFormFile("file", "utterance.wav")
	_, _ = f.Write(wav)
	language = whisperLanguage(language)
	_ = mw.WriteField("language", language)
	_ = mw.WriteField("translate", "false")
	_ = mw.WriteField("response_format", "verbose_json")
	_ = mw.WriteField("no_language_probabilities", "true")
	_ = mw.WriteField("no_timestamps", "true")
	for k, v := range w.Options {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, w.BaseURL+"/inference", &body)
	if e != nil {
		return provider.STTResult{}, e
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	r, e := w.Client.Do(req)
	if e != nil {
		return provider.STTResult{}, e
	}
	defer r.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if r.StatusCode != http.StatusOK {
		return provider.STTResult{}, fmt.Errorf("whisper %s: %s", r.Status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Text     string `json:"text"`
		Language string `json:"language"`
	}
	if e = json.Unmarshal(raw, &out); e != nil {
		return provider.STTResult{}, fmt.Errorf("decode whisper response: %w", e)
	}
	out.Text = strings.TrimSpace(out.Text)
	if isNonSpeech(out.Text) {
		return provider.STTResult{}, fmt.Errorf("no speech recognized")
	}
	out.Language = languageCode(out.Language)
	if out.Language == "" {
		out.Language = language
	}
	return provider.STTResult{Text: out.Text, Language: out.Language}, nil
}

// nonSpeechTag matches Whisper's annotations such as "(speaking in foreign
// language)", "[BLANK_AUDIO]" or "[Music]".
var nonSpeechTag = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)`)

// isNonSpeech reports whether the transcript has no words outside annotations,
// so a tag is never forwarded to the LLM as if the user had said it.
func isNonSpeech(text string) bool {
	return strings.Trim(nonSpeechTag.ReplaceAllString(text, ""), " \t\n.,!?-…") == ""
}

func whisperLanguage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "en", "hi":
		return strings.ToLower(strings.TrimSpace(language))
	case "hinglish":
		return "hi"
	default:
		return "auto"
	}
}

func languageCode(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "en", "english":
		return "en"
	case "hi", "hindi":
		return "hi"
	case "auto", "":
		return strings.ToLower(strings.TrimSpace(language))
	default:
		return strings.ToLower(strings.TrimSpace(language))
	}
}
