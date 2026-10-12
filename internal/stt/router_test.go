package stt

import (
	"context"
	"net/http"
	"testing"
)

func TestRouterSendsEnglishToFastModelAndRestToMultilingual(t *testing.T) {
	hits := map[string]string{}
	server := func(name string) *Whisper {
		return New("http://"+name, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			_ = r.ParseMultipartForm(1 << 20)
			hits[r.FormValue("language")] = name
			return response(http.StatusOK, `{"text":"ok","language":"x"}`), nil
		})})
	}
	r := &Router{English: server("small"), Other: server("turbo"), EnglishModel: "small.bin", OtherModel: "turbo.bin"}
	for _, language := range []string{"en", "hi", "hinglish", "auto"} {
		if _, err := r.Transcribe(context.Background(), make([]byte, 6400), language); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]string{"en": "small", "hi": "turbo", "auto": "turbo"} // hinglish is sent as hi
	for language, name := range want {
		if hits[language] != name {
			t.Errorf("%s went to %q, want %q (%v)", language, hits[language], name, hits)
		}
	}
	if r.Model("en") != "small.bin" || r.Model("hi") != "turbo.bin" || r.Model("auto") != "turbo.bin" {
		t.Fatal("Model() must name the routed model")
	}
	single := &Router{English: server("small"), EnglishModel: "small.bin"}
	if _, err := single.Transcribe(context.Background(), make([]byte, 6400), "hi"); err != nil || hits["hi"] != "small" || single.Model("hi") != "small.bin" {
		t.Fatalf("without a multilingual server everything must use the English server: %v %v", hits, err)
	}
}
