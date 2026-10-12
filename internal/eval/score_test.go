package eval

import (
	"math"
	"testing"
)

func TestCompareCountsEditTypes(t *testing.T) {
	for _, c := range []struct {
		hyp           string
		sub, del, ins int
	}{{"one two three four", 0, 0, 0}, {"one six three four", 1, 0, 0}, {"one three four", 0, 1, 0}, {"one two three five four", 0, 0, 1}} {
		s := Compare("one two three four", c.hyp, "en")
		if s.Substitutions != c.sub || s.Deletions != c.del || s.Insertions != c.ins || s.RefWords != 4 {
			t.Fatalf("%q: got %+v", c.hyp, s)
		}
		if want := float64(c.sub+c.del+c.ins) / 4; math.Abs(s.WER-want) > 1e-9 || s.Exact != (want == 0) {
			t.Fatalf("%q: WER = %v exact=%v", c.hyp, s.WER, s.Exact)
		}
	}
}

func TestNormalizeEnglishAndHindi(t *testing.T) {
	if got := Normalize("Unfortunately, one-hundred  percent!"); got != "unfortunately one hundred percent" {
		t.Fatalf("english: %q", got)
	}
	if got := Normalize("मार्ग जैसा ही सोचें। (लंबी)"); got != "मार्ग जैसा ही सोचें लंबी" {
		t.Fatalf("hindi: %q", got)
	}
	if !Compare("कल सुबह दस बजे", "कल सुबह, दस बजे।", "hi").Exact {
		t.Fatal("punctuation and danda must not count as errors")
	}
}

func TestScriptsAreNotEquated(t *testing.T) {
	s := Compare("कल सुबह क्लास है", "kal subah class hai", "hi")
	if s.WER != 1 || !s.ScriptMismatch || s.Script != "latin" {
		t.Fatalf("Latin transliteration must not match Devanagari: %+v", s)
	}
	if s = Compare("मुझे कंप्यूटर समझाओ", "مجھے کمپیوٹر سمجھاؤ", "hi"); !s.ScriptMismatch || s.Script != "arabic" {
		t.Fatalf("Urdu script output must be flagged: %+v", s)
	}
}

func TestHinglishTechnicalWords(t *testing.T) {
	s := Compare("अब हम Python में loop लिखेंगे", "अब हम Python में लूप लिखेंगे", "hinglish")
	if s.TechnicalWords != 2 || s.TechnicalRecognized != 1 || s.ScriptMismatch {
		t.Fatalf("got %+v", s)
	}
}

func TestSummarize(t *testing.T) {
	s := Summarize([]float64{5, 1, 3, 2, 4})
	if s.N != 5 || s.Mean != 3 || s.Median != 3 || math.Abs(s.P95-4.8) > 1e-9 {
		t.Fatalf("got %+v", s)
	}
	if (Summarize(nil) != Stats{}) {
		t.Fatal("empty input")
	}
}
