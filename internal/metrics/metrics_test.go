package metrics

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMetricNamesAreExplicit(t *testing.T) {
	b, err := json.Marshal(Turn{STTMS: 1, FirstAudioMS: 2})
	if err != nil || !strings.Contains(string(b), `"first_audio_delivery_ms":2`) {
		t.Fatalf("metrics: %s, %v", b, err)
	}
}
