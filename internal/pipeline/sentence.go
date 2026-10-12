package pipeline

import "strings"

type Splitter struct{ pending string }

func (s *Splitter) Push(delta string) []string {
	s.pending += delta
	var out []string
	start := 0
	for i, r := range s.pending {
		if strings.ContainsRune(".!?।\n", r) {
			end := i + len(string(r))
			if t := strings.TrimSpace(s.pending[start:end]); t != "" {
				out = append(out, t)
			}
			start = end
		}
	}
	s.pending = s.pending[start:]
	return out
}
func (s *Splitter) Flush() string { v := strings.TrimSpace(s.pending); s.pending = ""; return v }
