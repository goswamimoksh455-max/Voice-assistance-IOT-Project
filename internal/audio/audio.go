package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"sort"
)

const WhisperSampleRate = 16000

type Stats struct {
	SampleRate     int     `json:"sample_rate"`
	Samples        int     `json:"samples"`
	DurationMS     int64   `json:"duration_ms"`
	PeakDBFS       float64 `json:"peak_dbfs"`
	RMSDBFS        float64 `json:"rms_dbfs"`
	ClippedSamples int     `json:"clipped_samples"`
	ClippedPercent float64 `json:"clipped_percent"`
}

func ValidatePCM16(pcm []byte, sampleRate, maxSeconds int) error {
	if sampleRate < 8000 || sampleRate > 192000 {
		return errors.New("unsupported sample rate")
	}
	if len(pcm) < sampleRate/5*2 {
		return errors.New("recording is too short")
	}
	if len(pcm)%2 != 0 || len(pcm) > sampleRate*maxSeconds*2 {
		return errors.New("invalid recording length")
	}
	if !HasSpeech(pcm, sampleRate) {
		return errors.New("no speech detected")
	}
	return nil
}

// Speech gating. A fixed loudness threshold rejected quiet but clear speech
// (FLEURS recordings at -62 dBFS RMS) while steady room noise is louder than
// that, so the gate measures the level spread instead: speech alternates
// syllables and pauses, noise does not. Measured on real recordings, speech
// spread was 17.5-50 dB and room/white noise 0.8-6.5 dB.
const (
	speechFrameMS     = 20
	speechSkipMS      = 150 // microphone warm-up is often near-silent
	minSpeechSpreadDB = 12
)

// HasSpeech reports whether PCM16 audio contains speech-like level variation.
func HasSpeech(pcm []byte, sampleRate int) bool {
	return LevelSpreadDB(pcm, sampleRate) >= minSpeechSpreadDB
}

// LevelSpreadDB is the difference between the 90th and 10th percentile of
// 20 ms frame energies, ignoring the first 150 ms when the clip is long enough.
func LevelSpreadDB(pcm []byte, sampleRate int) float64 {
	frame := sampleRate * speechFrameMS / 1000
	samples := toFloat(pcm)
	if skip := sampleRate * speechSkipMS / 1000; len(samples) >= skip+5*frame {
		samples = samples[skip:]
	}
	var levels []float64
	for i := 0; i+frame <= len(samples); i += frame {
		var sum float64
		for _, v := range samples[i : i+frame] {
			sum += v * v
		}
		levels = append(levels, 10*math.Log10(sum/float64(frame)+1e-3)) // floor below 1 LSB of energy
	}
	if len(levels) == 0 {
		return 0
	}
	sort.Float64s(levels)
	at := func(p float64) float64 { return levels[int(math.Round(p*float64(len(levels)-1)))] }
	return at(0.9) - at(0.1)
}

func WAV(pcm []byte, sampleRate int) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(sampleRate*2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}

// AnalyzePCM16 reports levels without altering the recording.
func AnalyzePCM16(pcm []byte, sampleRate int) Stats {
	s := Stats{SampleRate: sampleRate, Samples: len(pcm) / 2}
	if sampleRate > 0 {
		s.DurationMS = int64(s.Samples) * 1000 / int64(sampleRate)
	}
	if s.Samples == 0 {
		s.PeakDBFS, s.RMSDBFS = math.Inf(-1), math.Inf(-1)
		return s
	}
	var sum, peak float64
	for i := 0; i < len(pcm); i += 2 {
		v := math.Abs(float64(int16(binary.LittleEndian.Uint16(pcm[i:i+2]))) / 32768)
		if v > peak {
			peak = v
		}
		if v >= 32767.0/32768.0 {
			s.ClippedSamples++
		}
		sum += v * v
	}
	s.ClippedPercent = float64(s.ClippedSamples) * 100 / float64(s.Samples)
	s.PeakDBFS = dbfs(peak)
	s.RMSDBFS = dbfs(math.Sqrt(sum / float64(s.Samples)))
	return s
}

func dbfs(v float64) float64 {
	if v <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(v)
}

// ResamplePCM16 performs complete-utterance, band-limited resampling. Processing
// the entire recording avoids discontinuities at WebSocket frame boundaries.
func ResamplePCM16(pcm []byte, inputRate, outputRate int) ([]byte, error) {
	if inputRate < 8000 || inputRate > 192000 || outputRate < 8000 || outputRate > 192000 {
		return nil, errors.New("unsupported sample rate")
	}
	if len(pcm)%2 != 0 {
		return nil, errors.New("PCM16 byte count must be even")
	}
	if inputRate == outputRate {
		return append([]byte(nil), pcm...), nil
	}
	inputSamples := len(pcm) / 2
	if inputSamples == 0 {
		return []byte{}, nil
	}
	outputSamples := int(math.Round(float64(inputSamples) * float64(outputRate) / float64(inputRate)))
	out := make([]byte, outputSamples*2)
	const halfTaps = 24
	cutoff := math.Min(1, float64(outputRate)/float64(inputRate)) * 0.94
	for i := 0; i < outputSamples; i++ {
		position := float64(i) * float64(inputRate) / float64(outputRate)
		center := int(math.Floor(position))
		var weighted, weights float64
		for sample := center - halfTaps + 1; sample <= center+halfTaps; sample++ {
			if sample < 0 || sample >= inputSamples {
				continue
			}
			distance := position - float64(sample)
			if math.Abs(distance) >= halfTaps {
				continue
			}
			window := 0.5 + 0.5*math.Cos(math.Pi*distance/halfTaps)
			weight := cutoff * sinc(cutoff*distance) * window
			value := float64(int16(binary.LittleEndian.Uint16(pcm[sample*2:])))
			weighted += value * weight
			weights += weight
		}
		if weights != 0 {
			weighted /= weights
		}
		weighted = math.Max(-32768, math.Min(32767, math.Round(weighted)))
		binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(weighted)))
	}
	return out, nil
}

func sinc(x float64) float64 {
	if math.Abs(x) < 1e-12 {
		return 1
	}
	p := math.Pi * x
	return math.Sin(p) / p
}

func PrepareWhisper(pcm []byte, inputRate, maxSeconds int) ([]byte, error) {
	if err := ValidatePCM16(pcm, inputRate, maxSeconds); err != nil {
		return nil, err
	}
	return ResamplePCM16(pcm, inputRate, WhisperSampleRate)
}

// DecodeWAV accepts uncompressed mono PCM16 WAV data, the same format used by
// browser debug downloads and Piper responses.
func DecodeWAV(wav []byte) ([]byte, int, error) {
	pcm, rate, format, err := DecodeToPCM16(wav)
	if err == nil && format != "pcm_s16le" {
		err = errors.New("WAV must be mono PCM16")
	}
	if err != nil {
		return nil, 0, err
	}
	return pcm, rate, nil
}

// DecodeToPCM16 accepts mono PCM16 or IEEE float32 WAV and returns PCM16. Float
// samples are converted exactly as the browser AudioWorklet converts them.
func DecodeToPCM16(wav []byte) (pcm []byte, sampleRate int, format string, err error) {
	if len(wav) < 12 || string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return nil, 0, "", errors.New("invalid RIFF/WAVE header")
	}
	var data []byte
	for offset := 12; offset+8 <= len(wav); {
		name := string(wav[offset : offset+4])
		size := int(binary.LittleEndian.Uint32(wav[offset+4 : offset+8]))
		start, end := offset+8, offset+8+size
		if end > len(wav) || end < start {
			return nil, 0, "", errors.New("truncated WAV chunk")
		}
		switch name {
		case "fmt ":
			if size < 16 {
				return nil, 0, "", errors.New("invalid fmt chunk")
			}
			tag, channels, bits := binary.LittleEndian.Uint16(wav[start:]), binary.LittleEndian.Uint16(wav[start+2:]), binary.LittleEndian.Uint16(wav[start+14:])
			switch {
			case channels != 1:
				return nil, 0, "", errors.New("WAV must be mono")
			case tag == 1 && bits == 16:
				format = "pcm_s16le"
			case tag == 3 && bits == 32:
				format = "pcm_f32le"
			default:
				return nil, 0, "", errors.New("WAV must be PCM16 or float32")
			}
			sampleRate = int(binary.LittleEndian.Uint32(wav[start+4 : start+8]))
		case "data":
			data = wav[start:end]
		}
		offset = end + size%2
	}
	if sampleRate == 0 || data == nil {
		return nil, 0, "", errors.New("WAV is missing valid fmt or data")
	}
	switch format {
	case "pcm_s16le":
		if len(data)%2 != 0 {
			return nil, 0, "", errors.New("odd PCM16 byte count")
		}
		return append([]byte(nil), data...), sampleRate, format, nil
	default:
		if len(data)%4 != 0 {
			return nil, 0, "", errors.New("float32 byte count not a multiple of 4")
		}
		pcm = make([]byte, len(data)/2)
		for i := 0; i < len(data)/4; i++ {
			v := math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
			binary.LittleEndian.PutUint16(pcm[i*2:], uint16(FloatToPCM16(float64(v))))
		}
		return pcm, sampleRate, format, nil
	}
}

// FloatToPCM16 mirrors worklet.js: clamp to [-1, 1], then scale negative values
// by 32768 and positive values by 32767 with rounding.
func FloatToPCM16(v float64) int16 {
	if math.IsNaN(v) {
		return 0
	}
	v = math.Max(-1, math.Min(1, v))
	if v < 0 {
		return int16(math.Round(v * 32768))
	}
	return int16(math.Round(v * 32767))
}

// Fidelity describes how closely received audio matches a reference recording
// at the same sample rate.
type Fidelity struct {
	LagMS         float64 `json:"lag_ms"`      // positive: received audio starts later
	Correlation   float64 `json:"correlation"` // normalized, at the best lag
	SNRDB         float64 `json:"snr_db"`      // after optimal gain, capped at MaxSNRDB
	DurationDelta int64   `json:"duration_delta_ms"`
}

// Align finds the lag (within maxLag samples) that best aligns got with ref and
// reports correlation and SNR over the overlap. The lag search runs on a 4x
// decimated signal and is refined at full rate, keeping it cheap for utterances.
func Align(ref, got []byte, sampleRate, maxLag int) Fidelity {
	r, g := toFloat(ref), toFloat(got)
	f := Fidelity{DurationDelta: int64(len(g)-len(r)) * 1000 / int64(max(1, sampleRate))}
	if len(r) == 0 || len(g) == 0 {
		return f
	}
	const step = 4
	best, bestLag := math.Inf(-1), 0
	for lag := -maxLag; lag <= maxLag; lag += step {
		if c := correlationAt(r, g, lag, step); c > best {
			best, bestLag = c, lag
		}
	}
	coarse := bestLag
	best = math.Inf(-1)
	for lag := coarse - step; lag <= coarse+step; lag++ {
		if c := correlationAt(r, g, lag, 1); c > best {
			best, bestLag = c, lag
		}
	}
	f.LagMS = float64(bestLag) * 1000 / float64(sampleRate)
	f.Correlation = correlationAt(r, g, bestLag, 1)
	var rr, rg, gg float64
	overlap(r, g, bestLag, 1, func(a, b float64) { rr += a * a; rg += a * b; gg += b * b })
	if rr > 0 {
		k := rg / rr
		f.SNRDB = MaxSNRDB // bit-identical audio; JSON cannot encode +Inf
		if noise := gg - 2*k*rg + k*k*rr; noise > 0 {
			f.SNRDB = math.Min(MaxSNRDB, 10*math.Log10(k*k*rr/noise))
		}
	}
	return f
}

// MaxSNRDB is reported for identical signals.
const MaxSNRDB = 200.0

func toFloat(pcm []byte) []float64 {
	out := make([]float64, len(pcm)/2)
	for i := range out {
		out[i] = float64(int16(binary.LittleEndian.Uint16(pcm[i*2:])))
	}
	return out
}

// overlap visits ref[i] and got[i+lag] for every stride-th overlapping sample.
func overlap(ref, got []float64, lag, stride int, fn func(a, b float64)) {
	for i := max(0, -lag); i < len(ref) && i+lag < len(got); i += stride {
		fn(ref[i], got[i+lag])
	}
}

func correlationAt(ref, got []float64, lag, stride int) float64 {
	var rr, rg, gg float64
	overlap(ref, got, lag, stride, func(a, b float64) { rr += a * a; rg += a * b; gg += b * b })
	if rr == 0 || gg == 0 {
		return 0
	}
	return rg / math.Sqrt(rr*gg)
}
