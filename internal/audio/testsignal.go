package audio

import (
	"encoding/binary"
	"math"
)

// SpeechLikePCM returns a deterministic speech-like test signal at sampleRate:
// 180 ms voiced "syllables" (harmonic tone with a smooth envelope) separated
// by 120 ms pauses, peaking at amplitude (0-1). Tests use it wherever audio
// must pass the speech gate; a steady tone would correctly be rejected.
func SpeechLikePCM(sampleRate int, seconds, amplitude float64) []byte {
	samples := int(float64(sampleRate) * seconds)
	pcm := make([]byte, samples*2)
	period := 0.3
	for i := 0; i < samples; i++ {
		t := float64(i) / float64(sampleRate)
		phase := math.Mod(t, period)
		env := 0.0
		if phase < 0.18 {
			env = math.Sin(math.Pi * phase / 0.18)
		}
		v := env * amplitude * (0.6*math.Sin(2*math.Pi*140*t) + 0.3*math.Sin(2*math.Pi*280*t) + 0.1*math.Sin(2*math.Pi*1200*t))
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(FloatToPCM16(v)))
	}
	return pcm
}
