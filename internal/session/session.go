package session

import (
	"context"
	"sync"
)

type Message struct{ Role, Text string }
type Session struct {
	mu          sync.Mutex
	maxMessages int
	history     []Message
	cancel      context.CancelFunc
}

func New(maxTurns int) *Session { return &Session{maxMessages: maxTurns * 2} }
func (s *Session) History() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.history...)
}
func (s *Session) Add(role, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, Message{role, text})
	if len(s.history) > s.maxMessages {
		s.history = append([]Message(nil), s.history[len(s.history)-s.maxMessages:]...)
	}
}
func (s *Session) Clear() { s.mu.Lock(); defer s.mu.Unlock(); s.history = nil }
func (s *Session) Begin(parent context.Context) context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	var ctx context.Context
	ctx, s.cancel = context.WithCancel(parent)
	return ctx
}
func (s *Session) Abort() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}
