package pipeline

import (
	"reflect"
	"testing"
)

func TestSplitter(t *testing.T) {
	var s Splitter
	got := append(s.Push("Hello. How"), s.Push(" are you? Fine")...)
	if tail := s.Flush(); tail != "" {
		got = append(got, tail)
	}
	want := []string{"Hello.", "How are you?", "Fine"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
}
