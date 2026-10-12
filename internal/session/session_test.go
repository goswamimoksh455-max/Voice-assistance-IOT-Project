package session

import (
	"context"
	"testing"
)

func TestHistoryBound(t *testing.T) {
	s := New(1)
	s.Add("user", "one")
	s.Add("assistant", "two")
	s.Add("user", "three")
	h := s.History()
	if len(h) != 2 || h[0].Text != "two" {
		t.Fatalf("unexpected history: %#v", h)
	}
}
func TestBeginCancelsPrevious(t *testing.T) {
	s := New(1)
	a := s.Begin(context.Background())
	_ = s.Begin(context.Background())
	select {
	case <-a.Done():
	default:
		t.Fatal("previous context not cancelled")
	}
}
