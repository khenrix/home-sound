package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/session"
	"github.com/khenrix/home-sound/internal/volume"
	"github.com/khenrix/home-sound/internal/wave"
)

func fixture() config.Config {
	return config.Config{Version: 1, Groups: map[string][]string{config.TVAndSpeakers: {"tv", "kitchen", "roam", "hall"}, config.AllSpeakers: {"kitchen", "roam", "hall"}}, Devices: map[string]config.Device{"tv": {Kind: "tv", Adapter: "airplay", PairingFile: "/private/secret-canary"}, "kitchen": {Kind: "speaker", Adapter: "airplay"}, "roam": {Kind: "speaker", Adapter: "airplay"}, "hall": {Kind: "speaker", Adapter: "sendspin"}}, Profiles: map[string]volume.Profile{}}
}
func newService(t *testing.T) *Service {
	t.Helper()
	c := fixture()
	path := filepath.Join(t.TempDir(), "state", "config.json")
	if e := config.Save(path, c); e != nil {
		t.Fatal(e)
	}
	clock, e := session.NewRawClock()
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(path, c, session.Freeze(clock, time.Now), func(context.Context, session.DeviceRequest) (session.Output, error) {
		t.Error("configuration/status contacted device factory")
		return nil, io.ErrClosedPipe
	})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s
}
func TestHTTPStatusPreviewAndProfileValidation(t *testing.T) {
	s := newService(t)
	for _, path := range []string{"/v1/status", "/v1/config", "/v1/events", "/v1/profiles"} {
		r := httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		if r.Code != 200 {
			t.Fatalf("%s: %d %s", path, r.Code, r.Body.String())
		}
		if strings.Contains(r.Body.String(), "secret-canary") {
			t.Fatal("private storage path exposed")
		}
	}
	bad := []string{`{"device":"tv","profile":{"version":2,"verified":false}}`, `{"device":"tv","profile":{"version":1,"verified":true,"points":[]}}`}
	for _, body := range bad {
		r := httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest("PUT", "/v1/profiles", strings.NewReader(body)))
		if r.Code != 400 {
			t.Fatalf("malformed profile status %d", r.Code)
		}
	}
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest("PUT", "/v1/profiles", strings.NewReader(`{"device":"tv","profile":{"version":1,"verified":false,"points":[]}}`)))
	if r.Code != 200 {
		t.Fatalf("unverified preview rejected: %d %s", r.Code, r.Body.String())
	}
	got, e := config.Load(s.path)
	if e != nil || got.Profiles["tv"].Version != 1 || got.Ready() {
		t.Fatalf("profile not atomically saved/unverified: %+v %v", got, e)
	}
}
func TestControlRejectsUnknownOrCredentialFields(t *testing.T) {
	s := newService(t)
	for _, body := range []string{`{"action":"unknown"}`, `{"action":"stop","password":"x"}`, `{} {}`, `{"action":"target","target_db":1e999}`, `{"action":"select","group":"All Speakers","input":"/missing"}`} {
		r := httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest("POST", "/v1/control", strings.NewReader(body)))
		if r.Code != 400 {
			t.Fatalf("accepted %s status %d", body, r.Code)
		}
	}
}
func TestActualUnixSocketPermissionsAndShutdown(t *testing.T) {
	s := newService(t)
	dir := filepath.Join(t.TempDir(), "socket")
	path := filepath.Join(dir, "operator.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, e := Listen(path)
	if e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions: %v %v", info, e)
	}
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener, s) }()
	client := Client(path)
	res, e := client.Get("http://unix/v1/config")
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var preview map[string]any
	if json.Unmarshal(raw, &preview) != nil || preview["ready"] != false {
		t.Fatalf("wrong unverified preview %s", raw)
	}
	req, _ := http.NewRequest("POST", "http://unix/v1/control", bytes.NewBufferString(`{"action":"stop"}`))
	res, e = client.Do(req)
	if e != nil || res.StatusCode != 200 {
		t.Fatalf("stop over actual socket: %v %v", res, e)
	}
	res.Body.Close()
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("service shutdown stuck")
	}
	if c, e := net.Dial("unix", path); e == nil {
		c.Close()
		t.Fatal("listener survived shutdown")
	}
}
func TestSocketDoesNotReplaceExistingFile(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	path := filepath.Join(dir, "operator.sock")
	os.WriteFile(path, []byte("keep"), 0600)
	if l, e := Listen(path); e == nil {
		l.Close()
		t.Fatal("existing path overwritten")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "keep" {
		t.Fatal("existing file damaged")
	}
}

func TestHTTPPauseResumeControlsRetainedSession(t *testing.T) {
	s := newService(t)
	clock, err := session.NewRawClock()
	if err != nil {
		t.Fatal(err)
	}
	s.engine, err = session.New(s.cfg, clock, session.Freeze(clock, time.Now).OffsetNS, func(context.Context, session.DeviceRequest) (session.Output, error) { return nil, io.ErrClosedPipe })
	if err != nil {
		t.Fatal(err)
	}
	var wav bytes.Buffer
	wave.Write(&wav, audio.Canonical, make([]float32, 48000*4*2))
	path := filepath.Join(t.TempDir(), "input.wav")
	os.WriteFile(path, wav.Bytes(), 0600)
	raw, _ := json.Marshal(map[string]any{"action": "select", "group": config.TVAndSpeakers, "input": path, "tv_calibration": 10})
	r := httptest.NewRecorder()
	s.ServeHTTP(r, httptest.NewRequest("POST", "/v1/control", bytes.NewReader(raw)))
	if r.Code != 200 {
		t.Fatalf("selection: %d %s", r.Code, r.Body.String())
	}
	for _, action := range []string{"pause", "resume"} {
		r = httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest("POST", "/v1/control", strings.NewReader(`{"action":"`+action+`"}`)))
		if r.Code != 200 {
			t.Fatalf("%s: %d %s", action, r.Code, r.Body.String())
		}
		var status session.Status
		if json.Unmarshal(r.Body.Bytes(), &status) != nil || !status.Active {
			t.Fatal("transport action discarded session")
		}
		if action == "pause" && !status.Paused {
			t.Fatal("pause not exposed")
		}
		if action == "resume" && !status.Resuming {
			t.Fatal("resume preparation not exposed")
		}
	}
}
