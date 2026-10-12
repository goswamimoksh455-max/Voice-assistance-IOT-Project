package protocol

import (
	"bytes"
	"testing"
)

func TestAudioFrameRoundTrip(t *testing.T) {
	p := []byte{1, 2, 3}
	want := Header{Kind: AudioInputPCM, TurnID: 42, Sequence: 7}
	got, data, e := DecodeAudio(EncodeAudio(want, p))
	if e != nil || got != want || !bytes.Equal(data, p) {
		t.Fatalf("round trip: %#v %v %v", got, data, e)
	}
}
func TestRejectBadFrame(t *testing.T) {
	if _, _, e := DecodeAudio([]byte("bad")); e == nil {
		t.Fatal("expected error")
	}
}
