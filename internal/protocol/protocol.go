package protocol

import (
	"encoding/binary"
	"encoding/json"
	"errors"
)

const Version = 1
const HeaderSize = 16

type AudioKind uint8

const (
	AudioInputPCM        AudioKind = 1
	AudioOutputWAV       AudioKind = 2
	AudioDebugInputWAV   AudioKind = 3
	AudioDebugWhisperWAV AudioKind = 4
)

type Header struct {
	Kind             AudioKind
	Flags            uint16
	TurnID, Sequence uint32
}

func EncodeAudio(h Header, payload []byte) []byte {
	b := make([]byte, HeaderSize+len(payload))
	copy(b[:4], "VAP1")
	b[4] = Version
	b[5] = byte(h.Kind)
	binary.BigEndian.PutUint16(b[6:8], h.Flags)
	binary.BigEndian.PutUint32(b[8:12], h.TurnID)
	binary.BigEndian.PutUint32(b[12:16], h.Sequence)
	copy(b[16:], payload)
	return b
}
func DecodeAudio(b []byte) (Header, []byte, error) {
	if len(b) < HeaderSize || string(b[:4]) != "VAP1" || b[4] != Version {
		return Header{}, nil, errors.New("invalid audio frame header")
	}
	h := Header{Kind: AudioKind(b[5]), Flags: binary.BigEndian.Uint16(b[6:8]), TurnID: binary.BigEndian.Uint32(b[8:12]), Sequence: binary.BigEndian.Uint32(b[12:16])}
	return h, b[16:], nil
}

type Event struct {
	Type              string `json:"type"`
	Version           int    `json:"version,omitempty"`
	TurnID            uint32 `json:"turn_id,omitempty"`
	Sequence          uint32 `json:"sequence,omitempty"`
	Language          string `json:"language,omitempty"`
	SampleRate        int    `json:"sample_rate,omitempty"`
	CaptureSampleRate int    `json:"capture_sample_rate,omitempty"`
	Debug             bool   `json:"debug,omitempty"`
	Text              string `json:"text,omitempty"`
	Code              string `json:"code,omitempty"`
	Message           string `json:"message,omitempty"`
	Metrics           any    `json:"metrics,omitempty"`
	Audio             any    `json:"audio,omitempty"`
	// Development tools only (VOICE_DEV_TOOLS): stop after transcription and
	// score the turn against a public-dataset fixture.
	STTOnly bool   `json:"stt_only,omitempty"`
	Fixture string `json:"fixture,omitempty"`
	Eval    any    `json:"eval,omitempty"`
}

func DecodeEvent(b []byte) (Event, error) { var e Event; err := json.Unmarshal(b, &e); return e, err }
