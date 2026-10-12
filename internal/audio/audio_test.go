package audio

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"testing"
)

func TestValidateAndWAV(t *testing.T) {
	pcm := SpeechLikePCM(16000, 0.5, 0.03)
	if e := ValidatePCM16(pcm, 16000, 2); e != nil {
		t.Fatal(e)
	}
	w := WAV(pcm, 16000)
	if string(w[:4]) != "RIFF" || binary.LittleEndian.Uint32(w[24:28]) != 16000 {
		t.Fatal("bad wav")
	}
}

func TestWAVHeaderAndDuration(t *testing.T) {
	pcm := sinePCM(48000, 1000, timeSeconds(0.25), 0.5)
	wav := WAV(pcm, 48000)
	checks := map[string]bool{
		"RIFF":      string(wav[0:4]) == "RIFF",
		"size":      binary.LittleEndian.Uint32(wav[4:8]) == uint32(len(wav)-8),
		"WAVE":      string(wav[8:12]) == "WAVE",
		"PCM":       binary.LittleEndian.Uint16(wav[20:22]) == 1,
		"mono":      binary.LittleEndian.Uint16(wav[22:24]) == 1,
		"rate":      binary.LittleEndian.Uint32(wav[24:28]) == 48000,
		"byte_rate": binary.LittleEndian.Uint32(wav[28:32]) == 96000,
		"bits":      binary.LittleEndian.Uint16(wav[34:36]) == 16,
		"data":      binary.LittleEndian.Uint32(wav[40:44]) == uint32(len(pcm)),
	}
	for name, ok := range checks {
		if !ok {
			t.Errorf("invalid WAV %s", name)
		}
	}
	decoded, rate, err := DecodeWAV(wav)
	if err != nil || rate != 48000 || len(decoded) != len(pcm) {
		t.Fatalf("decode: rate=%d bytes=%d err=%v", rate, len(decoded), err)
	}
}

func TestResampleKnownTone(t *testing.T) {
	for _, inputRate := range []int{44100, 48000} {
		input := sinePCM(inputRate, 1000, timeSeconds(1), 0.7)
		got, err := ResamplePCM16(input, inputRate, WhisperSampleRate)
		if err != nil {
			t.Fatal(err)
		}
		if len(got)/2 != WhisperSampleRate {
			t.Fatalf("%d Hz: got %d samples", inputRate, len(got)/2)
		}
		crossings := 0
		previous := int16(binary.LittleEndian.Uint16(got[0:2]))
		for i := 2; i < len(got); i += 2 {
			current := int16(binary.LittleEndian.Uint16(got[i : i+2]))
			if previous <= 0 && current > 0 {
				crossings++
			}
			previous = current
		}
		if crossings < 998 || crossings > 1002 {
			t.Fatalf("%d Hz: tone changed to about %d Hz", inputRate, crossings)
		}
		stats := AnalyzePCM16(got, WhisperSampleRate)
		if stats.DurationMS != 1000 || stats.ClippedSamples != 0 || stats.PeakDBFS > -2.9 || stats.PeakDBFS < -3.3 {
			t.Fatalf("%d Hz stats: %#v", inputRate, stats)
		}
	}
}

func TestResamplePreservesChronologyAcrossFormerFrameBoundaries(t *testing.T) {
	input := make([]byte, 48000*2)
	for i := 0; i < 48000; i++ {
		binary.LittleEndian.PutUint16(input[i*2:], uint16(int16(i/12000*1000)))
	}
	whole, err := ResamplePCM16(input, 48000, 16000)
	if err != nil {
		t.Fatal(err)
	}
	if len(whole) != 32000 {
		t.Fatalf("got %d bytes", len(whole))
	}
	for quarter, want := range []int16{0, 1000, 2000, 3000} {
		index := quarter*4000 + 2000
		got := int16(binary.LittleEndian.Uint16(whole[index*2:]))
		if math.Abs(float64(got-want)) > 2 {
			t.Fatalf("quarter %d: got %d want %d", quarter, got, want)
		}
	}
}

type timeSeconds float64

func sinePCM(sampleRate, frequency int, seconds timeSeconds, amplitude float64) []byte {
	samples := int(float64(sampleRate) * float64(seconds))
	pcm := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		v := int16(math.Round(math.Sin(2*math.Pi*float64(frequency)*float64(i)/float64(sampleRate)) * amplitude * 32767))
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(v))
	}
	return pcm
}
func TestSilenceRejected(t *testing.T) {
	if e := ValidatePCM16(make([]byte, 16000), 16000, 2); e == nil {
		t.Fatal("expected silence error")
	}
}

// Regression: quiet speech used to be rejected by a fixed RMS threshold of 90
// (about -51 dBFS) although Whisper transcribes it correctly.
func TestQuietSpeechAcceptedAndSteadyNoiseRejected(t *testing.T) {
	quiet := SpeechLikePCM(16000, 3, 0.0015) // syllable peaks near -56 dBFS
	if s := AnalyzePCM16(quiet, 16000); s.RMSDBFS > -60 {
		t.Fatalf("fixture not quiet enough: %.1f dBFS", s.RMSDBFS)
	}
	if e := ValidatePCM16(quiet, 16000, 30); e != nil {
		t.Fatalf("quiet speech rejected: %v (spread %.1f dB)", e, LevelSpreadDB(quiet, 16000))
	}
	noise := make([]byte, 3*16000*2) // steady noise far louder than the quiet speech
	state := uint32(1)
	for i := 0; i < len(noise); i += 2 {
		state = state*1664525 + 1013904223
		binary.LittleEndian.PutUint16(noise[i:], uint16(int16(int32(state>>16)-32768)/40))
	}
	if e := ValidatePCM16(noise, 16000, 30); e == nil {
		t.Fatalf("steady noise accepted (spread %.1f dB)", LevelSpreadDB(noise, 16000))
	}
	warmup := append(make([]byte, 16000/10*2), noise[:16000*2]...) // near-silent mic start, then noise
	if HasSpeech(warmup, 16000) {
		t.Fatalf("microphone warm-up made noise look like speech (spread %.1f dB)", LevelSpreadDB(warmup, 16000))
	}
}

func TestDecodeFloat32WAVMatchesWorkletConversion(t *testing.T) {
	samples := []float32{0, 0.5, -0.5, 1, -1, 1.5, -1.5, 0.25}
	data := make([]byte, len(samples)*4)
	for i, v := range samples {
		binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(v))
	}
	var wav []byte
	wav = append(wav, "RIFF\x00\x00\x00\x00WAVEfmt "...)
	fmtChunk := make([]byte, 22) // 18-byte fmt (cbSize) as written by FLEURS
	binary.LittleEndian.PutUint32(fmtChunk[0:], 18)
	binary.LittleEndian.PutUint16(fmtChunk[4:], 3)
	binary.LittleEndian.PutUint16(fmtChunk[6:], 1)
	binary.LittleEndian.PutUint32(fmtChunk[8:], 16000)
	binary.LittleEndian.PutUint16(fmtChunk[18:], 32)
	wav = append(wav, fmtChunk...)
	wav = append(wav, "fact\x04\x00\x00\x00\x08\x00\x00\x00data"...)
	wav = binary.LittleEndian.AppendUint32(wav, uint32(len(data)))
	wav = append(wav, data...)
	pcm, rate, format, err := DecodeToPCM16(wav)
	if err != nil || rate != 16000 || format != "pcm_f32le" {
		t.Fatalf("rate=%d format=%s err=%v", rate, format, err)
	}
	want := []int16{0, 16384, -16384, 32767, -32768, 32767, -32768, 8192}
	for i, w := range want {
		if got := int16(binary.LittleEndian.Uint16(pcm[i*2:])); got != w {
			t.Fatalf("sample %d (%v): got %d want %d", i, samples[i], got, w)
		}
	}
	if _, _, err = DecodeWAV(wav); err == nil {
		t.Fatal("DecodeWAV must keep rejecting non-PCM16 input")
	}
}

func TestAlignDetectsLagAndCorruption(t *testing.T) {
	ref := make([]byte, 16000*2)
	for i := 0; i < 16000; i++ { // chirp: unambiguous alignment
		v := math.Sin(2 * math.Pi * (200*float64(i)/16000 + 600*math.Pow(float64(i)/16000, 2)))
		binary.LittleEndian.PutUint16(ref[i*2:], uint16(int16(v*12000)))
	}
	delayed := append(make([]byte, 160*2), ref...) // 10 ms later
	f := Align(ref, delayed, 16000, 4000)
	if math.Abs(f.LagMS-10) > 0.1 || f.Correlation < 0.999 || f.SNRDB < 40 || f.DurationDelta != 10 {
		t.Fatalf("delayed copy: %+v", f)
	}
	stretched, err := ResamplePCM16(ref, 16000, 22050) // played as 16 kHz: slowed 1.38x
	if err != nil {
		t.Fatal(err)
	}
	if f = Align(ref, stretched, 16000, 4000); f.Correlation > 0.5 {
		t.Fatalf("unresampled audio must not align: %+v", f)
	}
}

// Regression: identical audio (16 kHz capture, no resampling) produced an
// infinite SNR that json.Marshal rejects, so the client received an empty event.
func TestAlignIdenticalAudioIsJSONEncodable(t *testing.T) {
	pcm := SpeechLikePCM(16000, 1, 0.3)
	f := Align(pcm, pcm, 16000, 800)
	if f.Correlation < 0.9999 || f.SNRDB != MaxSNRDB || f.LagMS != 0 {
		t.Fatalf("identical audio: %+v", f)
	}
	if _, err := json.Marshal(f); err != nil {
		t.Fatal(err)
	}
}
