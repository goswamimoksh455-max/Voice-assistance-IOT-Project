package config

import (
	"errors"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Addr, WhisperURL, WhisperModel, WhisperOptions, GeminiKey, GeminiModel, ThinkingLevel string
	// Optional second whisper-server for Hindi, Hinglish and auto-detection.
	WhisperMultilingualURL, WhisperMultilingualModel   string
	PiperENURL, PiperHIURL, PiperENVoice, PiperHIVoice string
	MaxRecordingSeconds, HistoryTurns, MaxOutputTokens int
	STTTimeout, LLMTimeout, TTSTimeout                 time.Duration
	AudioDebug                                         bool
	// DevTools enables the localhost-only STT evaluation dashboard under /dev/.
	DevTools               bool
	FixturesDir, ReportDir string
}

func Load() (Config, error) {
	c := Config{
		Addr: env("VOICE_ADDR", "127.0.0.1:8090"), WhisperURL: env("WHISPER_URL", "http://127.0.0.1:8081"), WhisperModel: env("WHISPER_MODEL", "models/whisper/ggml-small.bin"),
		WhisperOptions:         env("WHISPER_OPTIONS", DefaultWhisperOptions),
		WhisperMultilingualURL: envOrUnset("WHISPER_MULTILINGUAL_URL", "http://127.0.0.1:8082"), WhisperMultilingualModel: env("WHISPER_MULTILINGUAL_MODEL", "models/whisper/ggml-large-v3-turbo-q5_0.bin"),
		GeminiKey: os.Getenv("GEMINI_API_KEY"), GeminiModel: env("GEMINI_MODEL", "gemini-3.5-flash-lite"), ThinkingLevel: env("GEMINI_THINKING_LEVEL", "minimal"),
		PiperENURL: env("PIPER_EN_URL", "http://127.0.0.1:5000"), PiperHIURL: env("PIPER_HI_URL", "http://127.0.0.1:5001"),
		PiperENVoice: env("PIPER_EN_VOICE", "en_US-lessac-medium"), PiperHIVoice: env("PIPER_HI_VOICE", "hi_IN-priyamvada-medium"),
		MaxRecordingSeconds: envInt("VOICE_MAX_RECORDING_SECONDS", 30), HistoryTurns: envInt("VOICE_HISTORY_TURNS", 8), MaxOutputTokens: envInt("GEMINI_MAX_OUTPUT_TOKENS", 220),
		STTTimeout: envDuration("VOICE_STT_TIMEOUT", 60*time.Second), LLMTimeout: envDuration("VOICE_LLM_TIMEOUT", 30*time.Second), TTSTimeout: envDuration("VOICE_TTS_TIMEOUT", 30*time.Second),
		AudioDebug: envBool("VOICE_AUDIO_DEBUG", false),
		DevTools:   envBool("VOICE_DEV_TOOLS", false), FixturesDir: env("VOICE_FIXTURES_DIR", "runtime/fixtures"), ReportDir: env("VOICE_REPORT_DIR", "runtime/eval"),
	}
	if c.MaxRecordingSeconds < 1 || c.MaxRecordingSeconds > 120 || c.HistoryTurns < 1 || c.MaxOutputTokens < 1 {
		return Config{}, errors.New("invalid numeric configuration")
	}
	if c.ThinkingLevel == "minimal" && c.GeminiModel == "gemini-3.8-flash" {
		return Config{}, errors.New("gemini-3.8-flash does not support minimal thinking; use low, medium, or high")
	}
	if c.ThinkingLevel != "minimal" && c.ThinkingLevel != "low" && c.ThinkingLevel != "medium" && c.ThinkingLevel != "high" {
		return Config{}, errors.New("GEMINI_THINKING_LEVEL must be minimal, low, medium, or high")
	}
	return c, nil
}

// DefaultWhisperOptions are the decoding parameters chosen by measurement on
// the evaluation fixtures (see docs/STT_EVALUATION.md).
const DefaultWhisperOptions = ""

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// envOrUnset is like env, but an explicitly empty value is kept (it disables
// an optional service) instead of falling back to the default.
func envOrUnset(k, d string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return d
}
func envInt(k string, d int) int {
	v, e := strconv.Atoi(os.Getenv(k))
	if e == nil {
		return v
	}
	return d
}
func envDuration(k string, d time.Duration) time.Duration {
	v, e := time.ParseDuration(os.Getenv(k))
	if e == nil {
		return v
	}
	return d
}
func envBool(k string, d bool) bool {
	v, e := strconv.ParseBool(os.Getenv(k))
	if e == nil {
		return v
	}
	return d
}

// AudioDebugAllowed reports whether a session may receive debug copies of its
// audio: the main UI's debug panel or the development evaluation tools.
func (c Config) AudioDebugAllowed() bool { return c.AudioDebug || c.DevTools }
