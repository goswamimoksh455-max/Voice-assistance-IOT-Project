// Command stt-bench runs the cached evaluation fixtures directly against a
// whisper-server (no browser, WebSocket or Go resampling) and reports WER, CER
// and latency per language. Use it to compare Whisper models and decoding
// options on identical recordings:
//
//	go run ./cmd/stt-bench -label small-default
//	go run ./cmd/stt-bench -whisper-url http://127.0.0.1:8082 -label base -options beam_size=5
//	go run ./cmd/stt-bench -compare runtime/eval/a.json,runtime/eval/b.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/akshatsingh/professor-voice-assistant/internal/eval"
	"github.com/akshatsingh/professor-voice-assistant/internal/stt"
)

type row struct {
	ID           string     `json:"id"`
	Language     string     `json:"language"`
	Source       string     `json:"source"`
	LanguageSent string     `json:"language_sent"`
	Reference    string     `json:"reference"`
	Text         string     `json:"text"`
	Error        string     `json:"error,omitempty"`
	STTMS        int64      `json:"stt_ms"`
	DurationMS   int64      `json:"duration_ms"`
	Score        eval.Score `json:"score"`
}

type summary struct {
	N, Failures, Exact, ScriptMismatch  int
	WER, CER, LatencyMS, RealTimeFactor eval.Stats
	Technical                           string `json:",omitempty"`
}

type report struct {
	Created    time.Time          `json:"created"`
	Label      string             `json:"label"`
	WhisperURL string             `json:"whisper_url"`
	Options    map[string]string  `json:"options"`
	Language   string             `json:"language_override,omitempty"`
	Summary    map[string]summary `json:"summary"`
	Rows       []row              `json:"rows"`
}

func main() {
	dir := flag.String("dir", "runtime/fixtures", "fixture directory")
	url := flag.String("whisper-url", "http://127.0.0.1:8081", "whisper-server URL")
	label := flag.String("label", "bench", "report label")
	options := flag.String("options", "", "extra whisper-server fields, e.g. beam_size=5,temperature_inc=0")
	only := flag.String("language", "", "only fixtures of this language (en, hi, hinglish)")
	override := flag.String("whisper-language", "", "language sent to Whisper instead of the fixture language (auto, en, hi)")
	n := flag.Int("n", 0, "maximum fixtures per language (0 = all)")
	out := flag.String("out", "runtime/eval", "report directory")
	compare := flag.String("compare", "", "comma-separated report files to print side by side, then exit")
	source := flag.String("source", "", "only fixtures from this source (FLEURS, OpenSLR-104, local); private local recordings are excluded unless named")
	flag.Parse()
	if *compare != "" {
		printComparison(strings.Split(*compare, ","))
		return
	}
	m, err := eval.LoadManifest(*dir)
	check(err)
	opts, err := stt.ParseOptions(*options)
	check(err)
	w := stt.New(*url, &http.Client{})
	w.Options = opts
	r := report{Created: time.Now(), Label: *label, WhisperURL: *url, Options: opts, Language: *override, Summary: map[string]summary{}}
	perLanguage := map[string]int{}
	for _, f := range m.Fixtures {
		if (*source == "" && f.Source == "local") || (*source != "" && f.Source != *source) || (*only != "" && f.Language != *only) || (*n > 0 && perLanguage[f.Language] >= *n) {
			continue
		}
		perLanguage[f.Language]++
		wav, err := os.ReadFile(filepath.Join(*dir, filepath.FromSlash(f.File)))
		check(err)
		language := f.Language
		if *override != "" {
			language = *override
		}
		if language == "hinglish" { // same mapping as the application (server.sttLanguage)
			language = "hi"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		start := time.Now()
		res, err := w.TranscribeWAV(ctx, wav, language)
		cancel()
		x := row{ID: f.ID, Language: f.Language, Source: f.Source, LanguageSent: language, Reference: f.Reference, STTMS: time.Since(start).Milliseconds(), DurationMS: f.DurationMS}
		if err != nil {
			x.Error = err.Error()
		} else {
			x.Text = res.Text
		}
		x.Score = eval.Compare(f.Reference, x.Text, f.Language)
		r.Rows = append(r.Rows, x)
		fmt.Fprintf(os.Stderr, "%-44s WER %5.1f%%  %5d ms  %s\n", f.ID, x.Score.WER*100, x.STTMS, firstNonEmpty(x.Error, x.Text))
	}
	for language := range perLanguage {
		r.Summary[language] = summarize(r.Rows, language)
	}
	check(os.MkdirAll(*out, 0o755))
	path := filepath.Join(*out, time.Now().Format("20060102-150405")+"-bench-"+regexp.MustCompile(`[^A-Za-z0-9_-]+`).ReplaceAllString(*label, "-")+".json")
	b, _ := json.MarshalIndent(r, "", "  ")
	check(os.WriteFile(path, b, 0o644))
	fmt.Println(path)
	printComparison([]string{path})
}

// summarize treats failed requests as 100% WER/CER so failures cannot improve averages.
func summarize(rows []row, language string) summary {
	var s summary
	var wer, cer, lat, rtf []float64
	tech, techOK := 0, 0
	for _, x := range rows {
		if x.Language != language {
			continue
		}
		s.N++
		if x.Error != "" {
			s.Failures++
			wer, cer = append(wer, 1), append(cer, 1)
		} else {
			wer, cer = append(wer, x.Score.WER), append(cer, x.Score.CER)
		}
		lat = append(lat, float64(x.STTMS))
		rtf = append(rtf, float64(x.STTMS)/float64(max(1, x.DurationMS)))
		if x.Score.Exact {
			s.Exact++
		}
		if x.Score.ScriptMismatch {
			s.ScriptMismatch++
		}
		tech += x.Score.TechnicalWords
		techOK += x.Score.TechnicalRecognized
	}
	s.WER, s.CER, s.LatencyMS, s.RealTimeFactor = eval.Summarize(wer), eval.Summarize(cer), eval.Summarize(lat), eval.Summarize(rtf)
	if tech > 0 {
		s.Technical = fmt.Sprintf("%d/%d", techOK, tech)
	}
	return s
}

func printComparison(paths []string) {
	fmt.Printf("%-34s %-9s %3s %13s %13s %16s %4s %5s %6s\n", "report", "language", "n", "WER mean/med", "CER mean/med", "latency med/p95", "fail", "mism", "tech")
	for _, p := range paths {
		b, err := os.ReadFile(strings.TrimSpace(p))
		check(err)
		var r report
		check(json.Unmarshal(b, &r))
		for _, language := range []string{"en", "hi", "hinglish"} {
			s, ok := r.Summary[language]
			if !ok {
				continue
			}
			fmt.Printf("%-34s %-9s %3d %6.1f/%5.1f%% %6.1f/%5.1f%% %7.0f/%6.0f ms %4d %5d %6s\n", trim(r.Label, 34), language, s.N,
				s.WER.Mean*100, s.WER.Median*100, s.CER.Mean*100, s.CER.Median*100, s.LatencyMS.Median, s.LatencyMS.P95, s.Failures, s.ScriptMismatch, s.Technical)
		}
	}
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return "ERROR " + a
	}
	return b
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
