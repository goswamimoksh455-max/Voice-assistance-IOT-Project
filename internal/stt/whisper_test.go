package stt

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Status: fmt.Sprintf("%d %s", code, http.StatusText(code)), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestWhisperFailureIsReported(t *testing.T) {
	c := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusServiceUnavailable, "model unavailable"), nil
	})}
	_, err := New("http://whisper.local", c).Transcribe(context.Background(), make([]byte, 6400), "auto")
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWhisperRequestAndResponse(t *testing.T) {
	c := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/inference" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		if r.FormValue("translate") != "false" {
			t.Error("translation must be disabled")
		}
		if r.FormValue("response_format") != "verbose_json" {
			t.Error("expected verbose_json")
		}
		return response(http.StatusOK, `{"text":" नमस्ते ","language":"Hindi"}`), nil
	})}
	got, err := New("http://whisper.local", c).Transcribe(context.Background(), make([]byte, 6400), "auto")
	if err != nil || got.Text != "नमस्ते" || got.Language != "hi" {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestWhisperAnnotationsAreNotSpeech(t *testing.T) {
	for _, text := range []string{"(speaking in foreign language)", "[BLANK_AUDIO]", " [Music] ", "(Speaking in foreign language)."} {
		c := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, fmt.Sprintf(`{"text":%q,"language":"en"}`, text)), nil
		})}
		if got, err := New("http://whisper.local", c).Transcribe(context.Background(), make([]byte, 6400), "en"); err == nil {
			t.Errorf("%q was accepted as speech: %#v", text, got)
		}
	}
	for _, text := range []string{"कल सुबह साडे दस्बजे मेरी गलास है", "What is O(n log n) complexity?"} {
		if isNonSpeech(text) {
			t.Errorf("%q rejected as non-speech", text)
		}
	}
}

func TestWhisperLanguageRouting(t *testing.T) {
	for input, want := range map[string]string{"en": "en", "hi": "hi", "auto": "auto", "hinglish": "hi", "": "auto"} {
		if got := whisperLanguage(input); got != want {
			t.Errorf("%q: got %q want %q", input, got, want)
		}
	}
}

func TestWhisperRequestCarriesLanguageAndOptions(t *testing.T) {
	var form map[string]string
	c := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		form = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			form[k] = v[0]
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		wav, _ := io.ReadAll(file)
		if header.Filename != "utterance.wav" || string(wav[:4]) != "RIFF" || len(wav) != 44+6400 {
			t.Errorf("unexpected upload %q (%d bytes)", header.Filename, len(wav))
		}
		return response(http.StatusOK, `{"text":"hello","language":"en"}`), nil
	})}
	w := New("http://whisper.local", c)
	var err error
	if w.Options, err = ParseOptions(" beam_size=5, temperature_inc=0 "); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Transcribe(context.Background(), make([]byte, 6400), "hinglish"); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"language": "hi", "translate": "false", "response_format": "verbose_json", "no_timestamps": "true", "beam_size": "5", "temperature_inc": "0"}
	for k, v := range want {
		if form[k] != v {
			t.Errorf("%s = %q, want %q (form %v)", k, form[k], v, form)
		}
	}
}

func TestParseOptionsRejectsManagedFields(t *testing.T) {
	for _, bad := range []string{"language=en", "translate=true", "beam_size", "=5"} {
		if _, err := ParseOptions(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if got, err := ParseOptions(""); err != nil || len(got) != 0 {
		t.Fatalf("empty options: %v %v", got, err)
	}
}
