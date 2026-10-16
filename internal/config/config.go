// Package config holds user preferences, stored as JSON in ~/.fox/config.json.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type Settings struct {
	// Level trades time for size, 1 (fast) to 9 (extreme).
	Level int `json:"level"`
	// Workers is the number of files processed concurrently. 0 = number of physical CPU cores.
	Workers            int  `json:"workers"`
	PreserveTimestamps bool `json:"preserveTimestamps"`
	LowPriority        bool `json:"lowPriority"`
	// SkipOptimized skips files fox has already produced with the same settings.
	SkipOptimized bool `json:"skipOptimized"`
	// Include and Exclude are glob patterns matched against the file name.
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
	// DisabledTools lists tool names that must never run.
	DisabledTools []string `json:"disabledTools"`
	// ToolPaths overrides how a tool is invoked, e.g. {"pngout": ["wine", "/opt/pngout.exe"]}.
	ToolPaths map[string][]string `json:"toolPaths"`

	JPEG    JPEG    `json:"jpeg"`
	PNG     Image   `json:"png"`
	GIF     Image   `json:"gif"`
	WebP    Image   `json:"webp"`
	TIFF    Meta    `json:"tiff"`
	BMP     Meta    `json:"bmp"`
	PDF     PDF     `json:"pdf"`
	Audio   Audio   `json:"audio"`
	Video   Video   `json:"video"`
	Archive Archive `json:"archive"`
	Web     Web     `json:"web"`
	Exe     Exe     `json:"exe"`

	// LeanifyIterations and PNGWolfIterations override the level derived value when > 0.
	LeanifyIterations int `json:"leanifyIterations"`
	PNGWolfIterations int `json:"pngwolfIterations"`
}

type Image struct {
	Lossy        bool `json:"lossy"`
	KeepMetadata bool `json:"keepMetadata"`
}

// Meta is for formats whose optimization is always lossless.
type Meta struct {
	KeepMetadata bool `json:"keepMetadata"`
}

type JPEG struct {
	Image
	// Arithmetic coding is smaller but not supported by all decoders.
	Arithmetic bool `json:"arithmetic"`
}

type PDF struct {
	// Profile is one of "none", "screen", "ebook", "printer", "prepress", "custom".
	// Anything but "none" re-renders the document with Ghostscript, which is lossy.
	Profile     string `json:"profile"`
	DPI         int    `json:"dpi"`
	SkipLayered bool   `json:"skipLayered"`
}

type Audio struct {
	KeepMetadata bool `json:"keepMetadata"`
	TrimSilence  bool `json:"trimSilence"`
}

type Video struct {
	KeepMetadata bool `json:"keepMetadata"`
}

type Archive struct {
	KeepMetadata bool `json:"keepMetadata"`
	// Recurse optimizes files inside archives as well.
	Recurse bool `json:"recurse"`
}

type Web struct {
	HTML bool `json:"html"`
	CSS  bool `json:"css"`
	JS   bool `json:"js"`
	SVG  bool `json:"svg"`
	// JSExtensions are extra file extensions (e.g. ".mjs") treated as JavaScript.
	JSExtensions []string `json:"jsExtensions"`
}

type Exe struct {
	Enabled bool `json:"enabled"`
	UPX     bool `json:"upx"`
	PETrim  bool `json:"peTrim"`
}

func Default() *Settings {
	return &Settings{
		Level:              5,
		PreserveTimestamps: true,
		LowPriority:        true,
		SkipOptimized:      true,
		PDF:                PDF{Profile: "none", DPI: 150},
		Web:                Web{SVG: true},
		Exe:                Exe{PETrim: true},
	}
}

// Dir returns ~/.fox.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".fox"), nil
}

func path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the settings file, returning defaults for anything missing.
func Load() (*Settings, error) {
	s := Default()
	p, err := path()
	if err != nil {
		return s, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		// Move the broken file aside, so the next Save cannot destroy the user's edits.
		bak := p + ".bad"
		if rerr := os.Rename(p, bak); rerr != nil {
			return Default(), fmt.Errorf("%s: %w", p, err)
		}
		return Default(), fmt.Errorf("%s: %w (moved to %s, using defaults)", p, err, bak)
	}
	s.Clamp()
	return s, nil
}

func (s *Settings) Save() error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Clamp forces values into their valid ranges.
func (s *Settings) Clamp() {
	s.Level = min(max(s.Level, 1), 9)
	s.Workers = max(s.Workers, 0)
	switch s.PDF.Profile {
	case "none", "screen", "ebook", "printer", "prepress", "custom":
	default:
		s.PDF.Profile = "none"
	}
	if s.PDF.DPI <= 0 {
		s.PDF.DPI = 150
	}
}

// Clone returns a deep copy, so running jobs are unaffected by later edits.
func (s *Settings) Clone() *Settings {
	b, _ := json.Marshal(s)
	c := &Settings{}
	_ = json.Unmarshal(b, c)
	return c
}

// Fingerprint identifies the settings that influence optimization output.
func (s *Settings) Fingerprint() string {
	c := s.Clone()
	c.Workers, c.PreserveTimestamps, c.LowPriority, c.SkipOptimized = 0, false, false, false
	c.Include, c.Exclude, c.ToolPaths = nil, nil, nil
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}
