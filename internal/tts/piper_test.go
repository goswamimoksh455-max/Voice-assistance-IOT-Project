package tts

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestVoiceRouting(t *testing.T) {
	p := New("en", "hi", "english", "hindi", nil)
	tests := []struct{ text, lang, want string }{{"Hello", "en", "english"}, {"नमस्ते", "auto", "hindi"}, {"Kaise hain?", "hinglish", "hindi"}}
	for _, tt := range tests {
		_, got := p.route(tt.text, tt.lang)
		if got != tt.want {
			t.Fatalf("%q: got %s", tt.text, got)
		}
	}
}

func TestProviderFailure(t *testing.T) {
	c := &http.Client{Transport: failureTransport{}}
	_, err := New("http://en.local", "http://hi.local", "en", "hi", c).Synthesize(context.Background(), "hello", "en")
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type failureTransport struct{}

func (failureTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable", Body: io.NopCloser(strings.NewReader("voice unavailable")), Header: make(http.Header)}, nil
}
