package metrics

type Turn struct {
	STTMS          int64  `json:"stt_ms"`
	LLMFirstTextMS int64  `json:"llm_first_text_ms"`
	LLMTotalMS     int64  `json:"llm_total_ms"`
	FirstTTSMS     int64  `json:"first_tts_ms"`
	FirstAudioMS   int64  `json:"first_audio_delivery_ms"`
	TotalMS        int64  `json:"total_ms"`
	Inference      string `json:"inference"`
}
