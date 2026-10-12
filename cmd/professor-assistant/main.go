package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/akshatsingh/professor-voice-assistant/internal/config"
	"github.com/akshatsingh/professor-voice-assistant/internal/llm"
	"github.com/akshatsingh/professor-voice-assistant/internal/server"
	"github.com/akshatsingh/professor-voice-assistant/internal/stt"
	"github.com/akshatsingh/professor-voice-assistant/internal/tts"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, e := config.Load()
	if e != nil {
		log.Error("configuration failed", "error", e)
		os.Exit(1)
	}
	ctx := context.Background()
	gem, e := llm.New(ctx, cfg.GeminiKey, cfg.GeminiModel, cfg.ThinkingLevel, cfg.MaxOutputTokens)
	if e != nil {
		log.Error("Gemini initialization failed", "error", e)
		os.Exit(1)
	}
	hc := &http.Client{}
	options, e := stt.ParseOptions(cfg.WhisperOptions)
	if e != nil {
		log.Error("configuration failed", "error", e)
		os.Exit(1)
	}
	speech := &stt.Router{English: stt.New(cfg.WhisperURL, hc), EnglishModel: cfg.WhisperModel}
	speech.English.Options = options
	if cfg.WhisperMultilingualURL != "" {
		speech.Other, speech.OtherModel = stt.New(cfg.WhisperMultilingualURL, hc), cfg.WhisperMultilingualModel
		speech.Other.Options = options
	}
	log.Info("whisper", "english_url", cfg.WhisperURL, "english_model", speech.Model("en"), "multilingual_url", cfg.WhisperMultilingualURL, "multilingual_model", speech.Model("hi"), "options", options)
	app := server.New(cfg, log, speech, gem, tts.New(cfg.PiperENURL, cfg.PiperHIURL, cfg.PiperENVoice, cfg.PiperHIVoice, hc))
	srv := &http.Server{Addr: cfg.Addr, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info("server listening", "addr", cfg.Addr)
		if e := srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
			log.Error("server failed", "error", e)
			os.Exit(1)
		}
	}()
	stop, _ := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	<-stop.Done()
	down, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(down)
}
