// Package control exposes filesystem-authenticated operator HTTP on a Unix socket.
package control

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"

	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/session"
	"github.com/khenrix/home-sound/internal/volume"
)

type Service struct {
	mu     sync.Mutex
	path   string
	cfg    config.Config
	engine *session.Engine
}

func New(path string, c config.Config, m session.Mapping, f session.Factory) (*Service, error) {
	engine, err := session.New(c, m.Clock, m.OffsetNS, f)
	if err != nil {
		return nil, err
	}
	return &Service{path: path, cfg: c, engine: engine}, nil
}
func (s *Service) Close() { s.mu.Lock(); defer s.mu.Unlock(); s.engine.Stop() }
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid request JSON")
	}
	return nil
}
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Method + " " + r.URL.Path {
	case "GET /v1/status":
		writeJSON(w, s.engine.Status())
	case "GET /v1/events":
		writeJSON(w, s.engine.Status().Events)
	case "GET /v1/config":
		type device struct {
			Kind       string  `json:"kind"`
			Adapter    string  `json:"adapter"`
			RoomTrimDB float64 `json:"room_trim_db"`
		}
		devices := map[string]device{}
		for id, d := range s.cfg.Devices {
			devices[id] = device{d.Kind, d.Adapter, d.RoomTrimDB}
		}
		writeJSON(w, map[string]any{"version": s.cfg.Version, "groups": s.cfg.Groups, "devices": devices, "ready": s.cfg.Ready(), "reference_verified": s.cfg.ReferenceVerified, "audibility": "unverified"})
	case "GET /v1/profiles":
		writeJSON(w, s.cfg.Profiles)
	case "PUT /v1/profiles":
		var update struct {
			Device  string         `json:"device"`
			Profile volume.Profile `json:"profile"`
		}
		if err := decode(w, r, &update); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if s.engine.Status().Active {
			http.Error(w, "stop the active session before replacing calibration", 409)
			return
		}
		next := s.cfg
		next.Profiles = map[string]volume.Profile{}
		for id, p := range s.cfg.Profiles {
			next.Profiles[id] = p
		}
		next.Profiles[update.Device] = update.Profile
		if err := next.Validate(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := config.Save(s.path, next); err != nil {
			http.Error(w, "profile storage failed", 500)
			return
		}
		err := s.engine.Reconfigure(next)
		if err != nil {
			http.Error(w, "profile configuration failed", 500)
			return
		}
		s.cfg = next
		writeJSON(w, map[string]any{"saved": true, "ready": next.Ready()})
	case "POST /v1/control":
		var command Command
		if err := decode(w, r, &command); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := s.control(command); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, s.engine.Status())
	default:
		http.NotFound(w, r)
	}
}

type Command struct {
	Action        string   `json:"action"`
	Group         string   `json:"group,omitempty"`
	Input         string   `json:"input,omitempty"`
	FirstSample   uint64   `json:"first_sample,omitempty"`
	TargetDB      *float64 `json:"target_db,omitempty"`
	TVCalibration *int     `json:"tv_calibration,omitempty"`
	Muted         *bool    `json:"muted,omitempty"`
	Epoch         uint64   `json:"epoch,omitempty"`
	Device        string   `json:"device,omitempty"`
}

func (s *Service) control(c Command) error {
	switch c.Action {
	case "select":
		f, err := os.Open(c.Input)
		if err != nil {
			return errors.New("cannot open reference WAV")
		}
		source, err := session.WAVAt(f, c.FirstSample)
		f.Close()
		if err != nil {
			return errors.New("invalid reference WAV or seek position")
		}
		_, err = s.engine.Select(session.Selection{Group: c.Group, Source: source, FirstSample: c.FirstSample, TargetDB: c.TargetDB, TVCalibration: c.TVCalibration})
		if err != nil {
			source.Close()
		}
		return err
	case "pause":
		return s.engine.Pause()
	case "resume":
		return s.engine.Resume()
	case "stop":
		s.engine.Stop()
		return nil
	case "target":
		if c.TargetDB == nil {
			return errors.New("target_db required")
		}
		return s.engine.SetTargetDB(*c.TargetDB)
	case "mute":
		if c.Muted == nil {
			return errors.New("muted required")
		}
		return s.engine.Mute(*c.Muted)
	case "exclude":
		if !s.engine.Exclude(c.Epoch, c.Device) {
			return errors.New("stale session or unknown member")
		}
		return nil
	default:
		return errors.New("unknown control action")
	}
}
