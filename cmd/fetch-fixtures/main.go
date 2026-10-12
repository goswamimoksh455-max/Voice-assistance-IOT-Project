// Command fetch-fixtures downloads a small, deterministic set of public speech
// recordings with reference transcripts for speech-to-text evaluation:
//
//   - Google FLEURS (CC BY 4.0), en_us and hi_in test splits, pinned revision.
//   - OpenSLR 104 (CC BY-SA 4.0), Hindi-English code-switched test set.
//
// Archives are streamed; only the selected recordings and metadata are written.
// Re-running reuses the cache and only adds missing fixtures.
package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/akshatsingh/professor-voice-assistant/internal/audio"
	"github.com/akshatsingh/professor-voice-assistant/internal/eval"
)

const (
	fleursRevision = "70bb2e84b976b7e960aa89f1c648e09c59f894dd"
	fleursBase     = "https://huggingface.co/datasets/google/fleurs/resolve/" + fleursRevision + "/data/"
	fleursPage     = "https://huggingface.co/datasets/google/fleurs"
	openslrPage    = "https://openslr.org/104/"
	openslrDefault = "https://openslr.elda.org/resources/104/Hindi-English_test.tar.gz"
)

// safeName accepts only plain file names, so archive entries can never choose
// a path outside the fixture directory.
var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var client = &http.Client{Timeout: 0}

func main() {
	dir := flag.String("dir", "runtime/fixtures", "fixture cache directory")
	en := flag.Int("en", 5, "FLEURS English (en_us) test recordings")
	hi := flag.Int("hi", 5, "FLEURS Hindi (hi_in) test recordings")
	hinglish := flag.Int("hinglish", 0, "OpenSLR 104 Hindi-English code-switched recordings (streams a 443 MB archive)")
	recordings := flag.Int("openslr-recordings", 3, "long OpenSLR source recordings to sample segments from")
	openslrURL := flag.String("openslr-url", openslrDefault, "OpenSLR 104 test archive mirror")
	add := flag.String("add", "", "add a local mono WAV recording (e.g. a debug-panel download) as a private fixture")
	reference := flag.String("reference", "", "with -add: what was actually said")
	addLanguage := flag.String("add-language", "", "with -add: en, hi or hinglish")
	flag.Parse()
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatal(err)
	}
	m, err := eval.LoadManifest(*dir)
	if err != nil {
		log.Fatal(err)
	}
	if *add != "" {
		*en, *hi, *hinglish = 0, 0, 0
	}
	for _, job := range []struct {
		lang, code string
		n          int
	}{{"en", "en_us", *en}, {"hi", "hi_in", *hi}} {
		if job.n > 0 {
			if err = fetchFLEURS(&m, *dir, job.lang, job.code, job.n); err != nil {
				log.Fatalf("FLEURS %s: %v", job.code, err)
			}
			check(m.Save(*dir))
		}
	}
	if *add != "" {
		check(addLocal(&m, *dir, *add, *reference, *addLanguage))
		check(m.Save(*dir))
	}
	if *hinglish > 0 {
		if err = fetchOpenSLR(&m, *dir, *openslrURL, *hinglish, *recordings); err != nil {
			log.Printf("OpenSLR 104: %v (English/Hindi fixtures are unaffected)", err)
		}
		check(m.Save(*dir))
	}
	fmt.Printf("manifest: %s (%d fixtures, %d skipped)\n", filepath.Join(*dir, eval.ManifestName), len(m.Fixtures), len(m.Skipped))
	for _, l := range []string{"en", "hi", "hinglish"} {
		n := 0
		for _, f := range m.Fixtures {
			if f.Language == l {
				n++
			}
		}
		fmt.Printf("  %-8s %d\n", l, n)
	}
}

type fleursRow struct {
	sentence, raw, normalized, gender string
	samples                           int
}

func fetchFLEURS(m *eval.Manifest, dir, lang, code string, n int) error {
	if m.Count(lang, "FLEURS") >= n {
		log.Printf("FLEURS %s: %d cached, nothing to download", code, m.Count(lang, "FLEURS"))
		return nil
	}
	out := filepath.Join(dir, "fleurs", code)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	tsvPath := filepath.Join(out, "test.tsv")
	if _, err := os.Stat(tsvPath); err != nil {
		if err = download(fleursBase+code+"/test.tsv", tsvPath); err != nil {
			return err
		}
	}
	rows, err := readFLEURSTSV(tsvPath)
	if err != nil {
		return err
	}
	resp, err := get(fleursBase + code + "/audio/test.tar.gz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	selected := 0
	for selected < n {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := path.Base(h.Name)
		if h.Typeflag != tar.TypeReg || !strings.HasSuffix(name, ".wav") {
			continue
		}
		row, ok := rows[name]
		if !ok || !safeName.MatchString(name) || !filepath.IsLocal(h.Name) {
			m.Skipped = appendSkip(m.Skipped, eval.Skipped{Source: "FLEURS", File: h.Name, Reason: "not listed in test.tsv or unsafe entry name"})
			continue
		}
		if seen[row.sentence] { // keep one speaker per sentence for variety
			continue
		}
		seen[row.sentence] = true
		id := "fleurs-" + code + "-" + strings.TrimSuffix(name, ".wav")
		if _, ok := m.Find(id); ok {
			selected++
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(tr, 64<<20))
		if err != nil {
			return err
		}
		pcm, rate, format, err := audio.DecodeToPCM16(raw)
		reason := ""
		switch {
		case err != nil:
			reason = err.Error()
		case rate != audio.WhisperSampleRate:
			reason = fmt.Sprintf("unexpected sample rate %d", rate)
		case row.samples > 0 && len(pcm)/2 != row.samples:
			reason = fmt.Sprintf("sample count %d differs from metadata %d", len(pcm)/2, row.samples)
		case len(pcm)/2 > 30*rate || len(pcm)/2 < rate:
			reason = "duration outside 1-30 s"
		case silent(pcm):
			reason = "digital silence (peak below -60 dBFS)"
		}
		if reason != "" {
			m.Skipped = appendSkip(m.Skipped, eval.Skipped{Source: "FLEURS", File: h.Name, Reason: reason})
			continue
		}
		f, err := writeFixture(dir, filepath.Join("fleurs", code, name), pcm, rate)
		if err != nil {
			return err
		}
		f.ID, f.Language, f.Source, f.SourceURL, f.Revision, f.License = id, lang, "FLEURS", fleursPage, fleursRevision, "CC-BY-4.0"
		f.OriginalFile, f.OriginalFormat = "data/"+code+"/audio/test.tar.gz:"+h.Name, format
		f.Reference, f.RawReference, f.Speaker = row.normalized, row.raw, row.gender
		m.Fixtures = append(m.Fixtures, f)
		selected++
		log.Printf("FLEURS %s: %s (%.1f s)", code, name, float64(f.DurationMS)/1000)
	}
	if selected < n {
		return fmt.Errorf("only %d of %d recordings available", selected, n)
	}
	return nil
}

// addLocal stores a private recording (resampled to 16 kHz with the
// application's resampler) so it can be scored like the public fixtures.
func addLocal(m *eval.Manifest, dir, file, reference, language string) error {
	if reference == "" || (language != "en" && language != "hi" && language != "hinglish") {
		return errors.New("-add needs -reference and -add-language en|hi|hinglish")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	pcm, rate, format, err := audio.DecodeToPCM16(raw)
	if err != nil {
		return err
	}
	if pcm, err = audio.ResamplePCM16(pcm, rate, audio.WhisperSampleRate); err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	id := "local-" + language + "-" + hex.EncodeToString(sum[:])[:12]
	if _, ok := m.Find(id); ok {
		log.Printf("%s already added as %s", file, id)
		return nil
	}
	f, err := writeFixture(dir, filepath.Join("local", id+".wav"), pcm, audio.WhisperSampleRate)
	if err != nil {
		return err
	}
	f.ID, f.Language, f.Source, f.SourceURL, f.License = id, language, "local", "", "private recording - do not distribute"
	f.OriginalFile, f.OriginalFormat, f.Reference = filepath.Base(file), fmt.Sprintf("%s %d Hz", format, rate), reference
	m.Fixtures = append(m.Fixtures, f)
	log.Printf("added %s as %s (%.1f s)", file, id, float64(f.DurationMS)/1000)
	return nil
}

func readFLEURSTSV(p string) (map[string]fleursRow, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows := map[string]fleursRow{}
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1<<20), 1<<20)
	for s.Scan() {
		c := strings.Split(s.Text(), "\t")
		if len(c) < 7 {
			continue
		}
		samples, _ := strconv.Atoi(c[5])
		rows[c[1]] = fleursRow{sentence: c[0], raw: c[2], normalized: c[3], samples: samples, gender: c[6]}
	}
	return rows, s.Err()
}

func fetchOpenSLR(m *eval.Manifest, dir, url string, n, maxRecordings int) error {
	if m.Count("hinglish", "OpenSLR-104") >= n {
		log.Printf("OpenSLR 104: %d cached, nothing to download", m.Count("hinglish", "OpenSLR-104"))
		return nil
	}
	out := filepath.Join(dir, "openslr104")
	tmp := filepath.Join(out, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(tmp) // long source recordings are never kept
	resp, err := get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	counter := &progress{r: resp.Body, total: resp.ContentLength, start: time.Now()}
	gz, err := gzip.NewReader(counter)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	meta := map[string][]byte{} // base name -> content, for small metadata files
	var kept []string
	for {
		if meta["text"] != nil && meta["segments"] != nil && len(kept) >= maxRecordings {
			log.Printf("OpenSLR 104: metadata and %d recordings found; stopping stream early", len(kept))
			break
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := path.Base(h.Name)
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if !filepath.IsLocal(h.Name) || !safeName.MatchString(name) {
			m.Skipped = appendSkip(m.Skipped, eval.Skipped{Source: "OpenSLR-104", File: h.Name, Reason: "unsafe entry name rejected"})
			continue
		}
		switch {
		case strings.HasSuffix(name, ".wav"):
			if len(kept) >= maxRecordings {
				continue
			}
			if err = writeFile(filepath.Join(tmp, name), io.LimitReader(tr, 512<<20)); err != nil {
				return err
			}
			kept = append(kept, strings.TrimSuffix(name, ".wav"))
		case h.Size <= 50<<20 && (name == "text" || name == "segments" || name == "wav.scp" || name == "utt2spk"):
			b, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			if meta[name] == nil {
				meta[name] = b
				log.Printf("OpenSLR 104: metadata %s (%d bytes)", h.Name, len(b))
			}
		}
	}
	if meta["text"] == nil || meta["segments"] == nil {
		return errors.New("archive has no text/segments metadata")
	}
	for _, name := range []string{"text", "segments"} {
		if err = os.WriteFile(filepath.Join(out, name), meta[name], 0o644); err != nil {
			return err
		}
	}
	texts := kaldiTable(meta["text"])
	type segment struct {
		utt, rec   string
		start, end float64
	}
	var candidates []segment
	keptSet := map[string]bool{}
	for _, r := range kept {
		keptSet[r] = true
	}
	for _, line := range strings.Split(string(meta["segments"]), "\n") {
		c := strings.Fields(line)
		if len(c) != 4 || !keptSet[c[1]] {
			continue
		}
		start, e1 := strconv.ParseFloat(c[2], 64)
		end, e2 := strconv.ParseFloat(c[3], 64)
		text := texts[c[0]]
		// Genuine code-switching: both Devanagari and Latin words, 2-15 s long.
		if e1 != nil || e2 != nil || end-start < 2 || end-start > 15 || !hasScript(text, 'ऀ', 'ॿ') || !hasLatinWord(text) {
			continue
		}
		candidates = append(candidates, segment{c[0], c[1], start, end})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].utt < candidates[j].utt })
	// Spread the selection across the kept recordings (speakers/topics).
	perRecording := map[string]int{}
	limit := (n + len(kept) - 1) / max(1, len(kept))
	recordings := map[string][]byte{}
	selected := m.Count("hinglish", "OpenSLR-104")
	for _, c := range candidates {
		if selected >= n {
			break
		}
		id := "openslr104-" + c.utt
		if _, ok := m.Find(id); ok || perRecording[c.rec] >= limit || !safeName.MatchString(c.utt) {
			continue
		}
		pcm, ok := recordings[c.rec]
		if !ok {
			raw, err := os.ReadFile(filepath.Join(tmp, c.rec+".wav"))
			if err != nil {
				return err
			}
			var rate int
			var format string
			pcm, rate, format, err = audio.DecodeToPCM16(raw)
			if err != nil || rate != audio.WhisperSampleRate {
				m.Skipped = appendSkip(m.Skipped, eval.Skipped{Source: "OpenSLR-104", File: c.rec + ".wav", Reason: fmt.Sprintf("unsupported audio (%s, %d Hz, %v)", format, rate, err)})
				keptSet[c.rec] = false
				continue
			}
			recordings[c.rec] = pcm
		}
		from, to := int(c.start*audio.WhisperSampleRate)*2, int(c.end*audio.WhisperSampleRate)*2
		if to > len(pcm) {
			m.Skipped = appendSkip(m.Skipped, eval.Skipped{Source: "OpenSLR-104", File: c.utt, Reason: "segment beyond end of recording"})
			continue
		}
		seg := pcm[from:to]
		if silent(seg) {
			m.Skipped = appendSkip(m.Skipped, eval.Skipped{Source: "OpenSLR-104", File: c.utt, Reason: "digital silence (peak below -60 dBFS)"})
			continue
		}
		f, err := writeFixture(dir, filepath.Join("openslr104", c.utt+".wav"), seg, audio.WhisperSampleRate)
		if err != nil {
			return err
		}
		f.ID, f.Language, f.Source, f.SourceURL, f.License = id, "hinglish", "OpenSLR-104", openslrPage, "CC-BY-SA-4.0"
		f.OriginalFile, f.OriginalFormat = path.Base(url)+":"+c.rec+".wav", "pcm_s16le"
		f.Reference, f.Segment = texts[c.utt], fmt.Sprintf("%.2f-%.2f", c.start, c.end)
		m.Fixtures = append(m.Fixtures, f)
		perRecording[c.rec]++
		selected++
		log.Printf("OpenSLR 104: %s (%.1f s): %s", c.utt, c.end-c.start, f.Reference)
	}
	if selected < n {
		return fmt.Errorf("only %d of %d code-switched segments found in %d recordings; raise -openslr-recordings", selected, n, len(kept))
	}
	return nil
}

// silent rejects only recordings without signal. Quiet speech is kept on
// purpose: it is a valid test of the application's level handling.
func silent(pcm []byte) bool {
	return audio.AnalyzePCM16(pcm, audio.WhisperSampleRate).PeakDBFS < -60
}

func kaldiTable(b []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

func hasScript(s string, lo, hi rune) bool {
	for _, r := range s {
		if r >= lo && r <= hi {
			return true
		}
	}
	return false
}

func hasLatinWord(s string) bool {
	for _, w := range strings.Fields(s) {
		if eval.DominantScript(w) == "latin" && len(w) > 1 {
			return true
		}
	}
	return false
}

func writeFixture(dir, rel string, pcm []byte, rate int) (eval.Fixture, error) {
	wav := audio.WAV(pcm, rate)
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(rel)), 0o755); err != nil {
		return eval.Fixture{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, rel), wav, 0o644); err != nil {
		return eval.Fixture{}, err
	}
	sum := sha256.Sum256(wav)
	samples := len(pcm) / 2
	return eval.Fixture{File: filepath.ToSlash(rel), SampleRate: rate, Samples: samples, DurationMS: int64(samples) * 1000 / int64(rate), SHA256: hex.EncodeToString(sum[:])}, nil
}

func appendSkip(list []eval.Skipped, s eval.Skipped) []eval.Skipped {
	for _, e := range list {
		if e == s {
			return list
		}
	}
	return append(list, s)
}

func get(url string) (*http.Response, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

func download(url, dst string) error {
	resp, err := get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return writeFile(dst, resp.Body)
}

func writeFile(dst string, r io.Reader) error {
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err = io.Copy(f, r); err == nil {
		err = f.Close()
	} else {
		f.Close()
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

type progress struct {
	r     io.Reader
	n     int64
	total int64
	next  int64
	start time.Time
}

func (p *progress) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if p.n >= p.next {
		log.Printf("OpenSLR 104: streamed %d / %d MB (%.0f s)", p.n>>20, p.total>>20, time.Since(p.start).Seconds())
		p.next += 50 << 20
	}
	return n, err
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
