// Package config holds versioned local configuration. Loading never contacts devices.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/pairing"
	"github.com/khenrix/home-sound/internal/volume"
)

const TVAndSpeakers = "TV + All Speakers"
const AllSpeakers = "All Speakers"

type Device struct {
	Transport         airplay.Transport `json:"transport,omitempty"`
	Kind              string            `json:"kind"`
	Adapter           string            `json:"adapter"`
	Host              string            `json:"host,omitempty"`
	Port              int               `json:"port,omitempty"`
	PairingFile       string            `json:"pairing_file,omitempty"`
	LGURL             string            `json:"lg_url,omitempty"`
	LGPairingFile     string            `json:"lg_pairing_file,omitempty"`
	CertificateSHA256 string            `json:"certificate_sha256,omitempty"`
	RoomTrimDB        float64           `json:"room_trim_db"`
}
type Config struct {
	Version            int                       `json:"version"`
	Groups             map[string][]string       `json:"groups"`
	Devices            map[string]Device         `json:"devices"`
	Profiles           map[string]volume.Profile `json:"profiles"`
	ReferenceCeilingDB *float64                  `json:"reference_ceiling_db,omitempty"`
	ReferenceVerified  bool                      `json:"reference_verified"`
	Helper             string                    `json:"helper,omitempty"`
	HelperSHA256       string                    `json:"helper_sha256,omitempty"`
}

func (c Config) Validate() error {
	if c.Version != 1 || len(c.Groups) != 2 || len(c.Devices) != 4 {
		return errors.New("version 1 requires exactly two destinations and four devices")
	}
	all, ok := c.Groups[AllSpeakers]
	if !ok || len(all) != 3 {
		return errors.New("All Speakers requires three speakers")
	}
	tvAll, ok := c.Groups[TVAndSpeakers]
	if !ok || len(tvAll) != 4 {
		return errors.New("TV + All Speakers requires TV and three speakers")
	}
	seen := map[string]bool{}
	for _, id := range all {
		d, ok := c.Devices[id]
		if !ok || seen[id] || id == "" || d.Kind != "speaker" {
			return errors.New("invalid speaker membership")
		}
		seen[id] = true
	}
	seen = map[string]bool{}
	tv := 0
	for _, id := range tvAll {
		d, ok := c.Devices[id]
		if !ok || seen[id] || id == "" {
			return errors.New("invalid full membership")
		}
		seen[id] = true
		if d.Kind == "tv" {
			tv++
		} else if d.Kind != "speaker" {
			return errors.New("invalid device kind")
		}
	}
	if tv != 1 {
		return errors.New("exactly one TV required")
	}
	for id, d := range c.Devices {
		if d.Adapter == "airplay" {
			if _, err := d.Transport.Normalize(); err != nil {
				return err
			}
		} else if d.Transport != (airplay.Transport{}) {
			return errors.New("transport route is only supported for AirPlay")
		}
		if d.Adapter != "airplay" && d.Adapter != "sendspin" {
			return errors.New("unsupported configured adapter")
		}
		if math.IsNaN(d.RoomTrimDB) || math.IsInf(d.RoomTrimDB, 0) || math.Abs(d.RoomTrimDB) > 30 {
			return errors.New("invalid room trim")
		}
		for _, path := range []string{d.PairingFile, d.LGPairingFile} {
			if path != "" && !filepath.IsAbs(path) {
				return errors.New("secret references must be absolute file paths")
			}
		}
		if p, ok := c.Profiles[id]; ok {
			if e := p.Validate(); e != nil {
				return e
			}
			if p.Verified && d.Adapter == "airplay" && p.Provenance.Transport != d.Transport.Fingerprint(c.HelperSHA256) {
				return errors.New("profile transport provenance mismatch; new measurement required")
			}
			if p.Verified && (p.Provenance.Adapter != d.Adapter || p.Provenance.Format != "48000/stereo/pcm16" || p.Provenance.NativeControl != nativeControl(d)) {
				return errors.New("profile transport/native-control fingerprint mismatch")
			}
		}
	}
	for id := range c.Profiles {
		if _, ok := c.Devices[id]; !ok {
			return errors.New("profile for unknown device")
		}
	}
	if c.ReferenceCeilingDB != nil && (math.IsNaN(*c.ReferenceCeilingDB) || math.IsInf(*c.ReferenceCeilingDB, 0)) {
		return errors.New("invalid acoustic ceiling")
	}
	if c.ReferenceVerified && c.ReferenceCeilingDB == nil {
		return errors.New("verified reference needs measured ceiling")
	}
	return nil
}
func nativeControl(d Device) string {
	if d.Kind == "tv" {
		return "webos"
	}
	return d.Adapter
}
func (c Config) Ready() bool {
	if c.Validate() != nil || !c.ReferenceVerified || c.ReferenceCeilingDB == nil {
		return false
	}
	for id := range c.Devices {
		if !c.Profiles[id].Verified {
			return false
		}
	}
	return true
}

// Save reuses the protected atomic storage boundary; configuration is limited to
// 16 KiB, including curves. A directory fsync makes the rename durable on Linux.
func Save(path string, c Config) error {
	if e := c.Validate(); e != nil {
		return e
	}
	if e := pairing.Save(path, c); e != nil {
		return e
	}
	dir, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
func Load(path string) (Config, error) {
	var raw json.RawMessage
	if e := pairing.Load(path, &raw); e != nil {
		return Config{}, e
	}
	var c Config
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&c); e != nil {
		return Config{}, errors.New("invalid config JSON")
	}
	if d.Decode(new(any)) != io.EOF {
		return Config{}, errors.New("trailing config data")
	}
	return c, c.Validate()
}
