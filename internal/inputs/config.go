// Package inputs owns explicitly enabled input runtimes and private identities.
package inputs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/source"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Version   int           `json:"version"`
	StateDir  string        `json:"state_dir"`
	BindIP    string        `json:"bind_ip"`
	Interface string        `json:"interface"`
	Spotify   SpotifyConfig `json:"spotify"`
	YouTube   YouTubeConfig `json:"youtube"`
	Volume    VolumeConfig  `json:"volume"`
}
type SpotifyConfig struct {
	ZeroconfPorts [2]int        `json:"zeroconf_ports"`
	Enabled       bool          `json:"enabled"`
	Helper        source.Helper `json:"helper"`
	Ports         [2]int        `json:"ports"`
}
type YouTubeConfig struct {
	Enabled        bool          `json:"enabled"`
	NativeAccepted bool          `json:"native_accepted"`
	Ports          [2]int        `json:"ports"`
	Mode           string        `json:"framing"`
	Resolver       source.Helper `json:"resolver"`
	Runtime        source.Helper `json:"runtime"`
	Decoder        source.Helper `json:"decoder"`
	DecoderVersion string        `json:"decoder_version"`
}
type VolumeConfig struct {
	MinDB   float64 `json:"min_db"`
	MaxDB   float64 `json:"max_db"`
	Initial int     `json:"initial_percent"`
}

func (v VolumeConfig) Target(n int) (float64, error) {
	if n < 0 || n > 100 || math.IsNaN(v.MinDB) || math.IsNaN(v.MaxDB) || math.IsInf(v.MinDB, 0) || math.IsInf(v.MaxDB, 0) || v.MinDB >= v.MaxDB || v.MinDB < -120 || v.MaxDB > 0 {
		return 0, errors.New("invalid common volume policy")
	}
	return v.MinDB + (v.MaxDB-v.MinDB)*float64(n)/100, nil
}
func (c Config) Validate() error {
	if c.Version != 1 {
		return errors.New("unsupported inputs version")
	}
	if !c.Spotify.Enabled && !c.YouTube.Enabled {
		return nil
	}
	if !filepath.IsAbs(c.StateDir) {
		return errors.New("absolute private input directory required")
	}
	if _, e := c.Volume.Target(c.Volume.Initial); e != nil {
		return e
	}
	ports := map[int]bool{}
	for _, p := range []struct {
		enabled bool
		ports   [2]int
	}{{c.Spotify.Enabled, c.Spotify.Ports}, {c.Spotify.Enabled, c.Spotify.ZeroconfPorts}, {c.YouTube.Enabled, c.YouTube.Ports}} {
		if p.enabled {
			for _, n := range p.ports {
				if n < 1024 || n > 65535 || ports[n] {
					return errors.New("distinct unprivileged input ports required")
				}
				ports[n] = true
			}
		}
	}
	if c.Spotify.Enabled && c.Interface == "" {
		return errors.New("explicit Spotify advertisement interface required")
	}
	if c.YouTube.Enabled {
		ip := net.ParseIP(c.BindIP)
		if ip == nil || !ip.IsPrivate() && !ip.IsLoopback() {
			return errors.New("explicit local DIAL address required")
		}
		if c.YouTube.Mode != "bytes" && c.YouTube.Mode != "utf16" {
			return errors.New("explicit Lounge framing required")
		}
	}
	return nil
}
func Load(path string) (Config, error) {
	var c Config
	if e := PrivateFile(path); e != nil {
		return c, e
	}
	f, e := os.Open(path)
	if e != nil {
		return c, e
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 16385))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF {
		return c, errors.New("invalid input configuration")
	}
	return c, c.Validate()
}
func PrivateDir(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("absolute private directory required")
	}
	for parent := path; ; parent = filepath.Dir(parent) {
		st, e := os.Lstat(parent)
		if e == nil {
			if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
				return errors.New("private state ancestors must be real directories")
			}
		} else if !os.IsNotExist(e) {
			return e
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}

	if e := os.MkdirAll(path, 0700); e != nil {
		return errors.New("private directory unavailable")
	}
	st, e := os.Lstat(path)
	if e != nil || !st.IsDir() || st.Mode().Perm() != 0700 {
		return errors.New("input directory must be private 0700")
	}
	return nil
}
func PrivateFile(path string) error {
	st, e := os.Lstat(path)
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		return errors.New("input file must be private regular 0600")
	}
	return nil
}
func Save(path string, b []byte) error {
	if e := PrivateDir(filepath.Dir(path)); e != nil {
		return e
	}
	if _, e := os.Lstat(path); e == nil {
		if e = PrivateFile(path); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".state-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}
func Nonce() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}

type Identity struct{ Group, Dir, DeviceID, UUID string }

func Prepare(c Config) ([]Identity, error) {
	if e := c.Validate(); e != nil {
		return nil, e
	}
	if !c.Spotify.Enabled && !c.YouTube.Enabled {
		return nil, nil
	}
	if e := PrivateDir(c.StateDir); e != nil {
		return nil, e
	}
	path := filepath.Join(c.StateDir, "identities.json")
	groups := []string{config.TVAndSpeakers, config.AllSpeakers}
	var ids []Identity
	if _, e := os.Lstat(path); e == nil {
		if e = PrivateFile(path); e != nil {
			return nil, e
		}
		f, e := os.Open(path)
		if e != nil {
			return nil, e
		}
		b, e := io.ReadAll(io.LimitReader(f, 4097))
		f.Close()
		if e != nil || len(b) > 4096 || json.Unmarshal(b, &ids) != nil || len(ids) != 2 {
			return nil, errors.New("invalid saved input identities")
		}
		for n, id := range ids {
			raw := strings.ReplaceAll(id.UUID, "-", "")
			_, ue := hex.DecodeString(raw)
			_, de := hex.DecodeString(id.DeviceID)
			if id.Group != groups[n] || id.Dir != filepath.Join(c.StateDir, fmt.Sprint(n)) || len(id.DeviceID) != 40 || de != nil || len(raw) != 32 || ue != nil {
				return nil, errors.New("invalid saved input identity")
			}
		}
		if ids[0].DeviceID == ids[1].DeviceID || ids[0].UUID == ids[1].UUID {
			return nil, errors.New("duplicate input identities")
		}
		return ids, nil
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	for n, g := range groups {
		x := Nonce()
		ids = append(ids, Identity{g, filepath.Join(c.StateDir, fmt.Sprint(n)), Nonce() + Nonce()[:8], x[:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:]})
	}
	b, _ := json.Marshal(ids)
	if e := Save(path, b); e != nil {
		return nil, e
	}
	return ids, nil
}
