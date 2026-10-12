package eval

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Score compares one transcript with its reference after normalization.
type Score struct {
	WER            float64 `json:"wer"`
	CER            float64 `json:"cer"`
	Exact          bool    `json:"exact"`
	RefWords       int     `json:"ref_words"`
	Substitutions  int     `json:"substitutions"`
	Deletions      int     `json:"deletions"`
	Insertions     int     `json:"insertions"`
	Script         string  `json:"script"` // dominant script of the hypothesis
	ScriptMismatch bool    `json:"script_mismatch"`
	// Hinglish only: Latin-script (mostly English technical) reference words
	// and how many appear verbatim, in Latin script, in the hypothesis.
	TechnicalWords      int `json:"technical_words,omitempty"`
	TechnicalRecognized int `json:"technical_recognized,omitempty"`
}

// Normalize prepares text for WER/CER: Unicode NFC, lower case, punctuation
// and symbols (including the danda) removed, whitespace collapsed. Scripts are
// never transliterated, so Latin Hindi never matches a Devanagari reference.
func Normalize(text string) string {
	text = norm.NFC.String(strings.ToLower(text))
	var b strings.Builder
	for _, r := range text {
		switch {
		case r == '-' || r == '–' || r == '—' || r == '/':
			b.WriteRune(' ')
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// Compare scores a hypothesis against a reference for the given language.
func Compare(reference, hypothesis, language string) Score {
	ref, hyp := Normalize(reference), Normalize(hypothesis)
	rw, hw := strings.Fields(ref), strings.Fields(hyp)
	s := Score{RefWords: len(rw), Exact: ref == hyp && ref != ""}
	s.Substitutions, s.Deletions, s.Insertions = alignCounts(rw, hw)
	if len(rw) > 0 {
		s.WER = float64(s.Substitutions+s.Deletions+s.Insertions) / float64(len(rw))
	}
	rc, hc := []rune(strings.ReplaceAll(ref, " ", "")), []rune(strings.ReplaceAll(hyp, " ", ""))
	if len(rc) > 0 {
		sub, del, ins := alignCounts(rc, hc)
		s.CER = float64(sub+del+ins) / float64(len(rc))
	}
	s.Script = DominantScript(hypothesis)
	switch language {
	case "en":
		s.ScriptMismatch = s.Script != "latin" && s.Script != "none"
	case "hi":
		s.ScriptMismatch = s.Script != "devanagari" && s.Script != "none"
	case "hinglish":
		s.ScriptMismatch = s.Script != "devanagari" && s.Script != "latin" && s.Script != "mixed" && s.Script != "none"
		hypLatin := map[string]bool{}
		for _, w := range hw {
			hypLatin[w] = true
		}
		for _, w := range rw {
			if DominantScript(w) == "latin" {
				s.TechnicalWords++
				if hypLatin[w] {
					s.TechnicalRecognized++
				}
			}
		}
	}
	return s
}

// alignCounts returns substitutions, deletions and insertions of the minimum
// edit-distance alignment from ref to hyp.
func alignCounts[T comparable](ref, hyp []T) (sub, del, ins int) {
	n, m := len(ref), len(hyp)
	d := make([][]int, n+1)
	for i := range d {
		d[i] = make([]int, m+1)
		d[i][0] = i
	}
	for j := 0; j <= m; j++ {
		d[0][j] = j
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			cost := 1
			if ref[i-1] == hyp[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j-1]+cost, d[i-1][j]+1, d[i][j-1]+1)
		}
	}
	for i, j := n, m; i > 0 || j > 0; {
		switch {
		case i > 0 && j > 0 && ref[i-1] == hyp[j-1] && d[i][j] == d[i-1][j-1]:
			i, j = i-1, j-1
		case i > 0 && j > 0 && d[i][j] == d[i-1][j-1]+1:
			sub++
			i, j = i-1, j-1
		case i > 0 && d[i][j] == d[i-1][j]+1:
			del++
			i--
		default:
			ins++
			j--
		}
	}
	return
}

// DominantScript classifies letters as latin, devanagari, arabic (Whisper's
// Urdu output for Hindi), other, mixed (no script above 80%) or none.
func DominantScript(text string) string {
	counts := map[string]int{}
	total := 0
	for _, r := range text {
		if !unicode.IsLetter(r) && !unicode.Is(unicode.Mn, r) && !unicode.Is(unicode.Mc, r) {
			continue
		}
		total++
		switch {
		case unicode.Is(unicode.Latin, r):
			counts["latin"]++
		case unicode.Is(unicode.Devanagari, r):
			counts["devanagari"]++
		case unicode.Is(unicode.Arabic, r):
			counts["arabic"]++
		default:
			counts["other"]++
		}
	}
	if total == 0 {
		return "none"
	}
	for script, c := range counts {
		if float64(c) >= 0.8*float64(total) {
			return script
		}
	}
	return "mixed"
}

// Stats summarizes a set of values.
type Stats struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	P95    float64 `json:"p95"`
}

func Summarize(values []float64) Stats {
	if len(values) == 0 {
		return Stats{}
	}
	v := append([]float64(nil), values...)
	sort.Float64s(v)
	sum := 0.0
	for _, x := range v {
		sum += x
	}
	return Stats{N: len(v), Mean: sum / float64(len(v)), Median: percentile(v, 0.5), P95: percentile(v, 0.95)}
}

// percentile uses linear interpolation between closest ranks of sorted v.
func percentile(v []float64, p float64) float64 {
	pos := p * float64(len(v)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return v[lo] + (v[hi]-v[lo])*(pos-float64(lo))
}
