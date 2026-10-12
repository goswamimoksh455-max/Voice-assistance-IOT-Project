// Package eval holds the speech-to-text evaluation fixtures and scoring used by
// the development test tools. Nothing here is used by the production path.
package eval

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

// Fixture is one public-dataset recording, stored as 16 kHz mono PCM16 WAV.
type Fixture struct {
	ID             string `json:"id"`
	Language       string `json:"language"` // en, hi or hinglish
	Source         string `json:"source"`   // FLEURS or OpenSLR-104
	SourceURL      string `json:"source_url"`
	Revision       string `json:"revision,omitempty"`
	License        string `json:"license"`
	OriginalFile   string `json:"original_file"`
	OriginalFormat string `json:"original_format"`
	File           string `json:"file"` // relative to the fixture directory
	Reference      string `json:"reference"`
	RawReference   string `json:"raw_reference,omitempty"`
	SampleRate     int    `json:"sample_rate"`
	Samples        int    `json:"samples"`
	DurationMS     int64  `json:"duration_ms"`
	SHA256         string `json:"sha256"`
	Speaker        string `json:"speaker,omitempty"`
	Segment        string `json:"segment,omitempty"` // OpenSLR: recording start-end seconds
}

// Skipped records a candidate recording that was rejected and why.
type Skipped struct {
	Source string `json:"source"`
	File   string `json:"file"`
	Reason string `json:"reason"`
}

type Manifest struct {
	Version  int       `json:"version"`
	Fixtures []Fixture `json:"fixtures"`
	Skipped  []Skipped `json:"skipped,omitempty"`
}

const ManifestName = "manifest.json"

func LoadManifest(dir string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{Version: 1}, nil
	}
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

// Save writes the manifest in a deterministic order.
func (m Manifest) Save(dir string) error {
	sort.SliceStable(m.Fixtures, func(i, j int) bool {
		if m.Fixtures[i].Language != m.Fixtures[j].Language {
			return m.Fixtures[i].Language < m.Fixtures[j].Language
		}
		return m.Fixtures[i].ID < m.Fixtures[j].ID
	})
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, ManifestName+".tmp")
	if err = os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, ManifestName))
}

func (m Manifest) Find(id string) (Fixture, bool) {
	for _, f := range m.Fixtures {
		if f.ID == id {
			return f, true
		}
	}
	return Fixture{}, false
}

// Count returns how many fixtures exist per language and source.
func (m Manifest) Count(language, source string) int {
	n := 0
	for _, f := range m.Fixtures {
		if f.Language == language && f.Source == source {
			n++
		}
	}
	return n
}
