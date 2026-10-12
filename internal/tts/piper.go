package tts

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/akshatsingh/professor-voice-assistant/internal/provider"
)

type Piper struct {
	ENURL, HIURL, ENVoice, HIVoice string
	Client                         *http.Client
}

func New(enURL, hiURL, enVoice, hiVoice string, c *http.Client) *Piper {
	return &Piper{strings.TrimRight(enURL, "/"), strings.TrimRight(hiURL, "/"), enVoice, hiVoice, c}
}
func (p *Piper) route(text, lang string) (string, string) {
	if lang == "hi" || lang == "hinglish" || hasDevanagari(text) {
		return p.HIURL, p.HIVoice
	}
	return p.ENURL, p.ENVoice
}
func hasDevanagari(s string) bool {
	for _, r := range s {
		if r >= 0x0900 && r <= 0x097f {
			return true
		}
	}
	return false
}
func (p *Piper) Ready(ctx context.Context) error {
	for _, u := range []string{p.ENURL, p.HIURL} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u+"/info", nil)
		r, e := p.Client.Do(req)
		if e != nil {
			return e
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			return fmt.Errorf("piper unavailable: %s", r.Status)
		}
	}
	return nil
}
func (p *Piper) Synthesize(ctx context.Context, text, lang string) (provider.TTSResult, error) {
	u, v := p.route(text, lang)
	payload, _ := json.Marshal(map[string]string{"text": text, "voice": v})
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, u+"/synthesize", bytes.NewReader(payload))
	if e != nil {
		return provider.TTSResult{}, e
	}
	req.Header.Set("Content-Type", "application/json")
	r, e := p.Client.Do(req)
	if e != nil {
		return provider.TTSResult{}, e
	}
	defer r.Body.Close()
	wav, _ := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if r.StatusCode != 200 {
		return provider.TTSResult{}, fmt.Errorf("piper %s: %s", r.Status, strings.TrimSpace(string(wav)))
	}
	if len(wav) < 44 || string(wav[:4]) != "RIFF" {
		return provider.TTSResult{}, fmt.Errorf("piper returned invalid WAV")
	}
	rate := int(binary.LittleEndian.Uint32(wav[24:28]))
	return provider.TTSResult{WAV: wav, SampleRate: rate, Voice: v}, nil
}
