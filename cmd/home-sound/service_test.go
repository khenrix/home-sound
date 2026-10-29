package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/control"
	"github.com/khenrix/home-sound/internal/session"
	"github.com/khenrix/home-sound/internal/volume"
)

func TestOperatorCLIOverUnixSocket(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "operator")
	path := filepath.Join(dir, "config.json")
	socket := filepath.Join(dir, "api.sock")
	cfg := config.Config{Version: 1, Groups: map[string][]string{config.TVAndSpeakers: {"tv", "kitchen", "roam", "hall"}, config.AllSpeakers: {"kitchen", "roam", "hall"}}, Devices: map[string]config.Device{"tv": {Kind: "tv", Adapter: "airplay"}, "kitchen": {Kind: "speaker", Adapter: "airplay"}, "roam": {Kind: "speaker", Adapter: "airplay"}, "hall": {Kind: "speaker", Adapter: "sendspin"}}, Profiles: map[string]volume.Profile{}}
	if e := config.Save(path, cfg); e != nil {
		t.Fatal(e)
	}
	clock, e := session.NewRawClock()
	if e != nil {
		t.Fatal(e)
	}
	s, e := control.New(path, cfg, session.Freeze(clock, time.Now), session.AirPlayFactory(cfg))
	if e != nil {
		t.Fatal(e)
	}
	l, e := control.Listen(socket)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- control.Serve(ctx, l, s) }()
	defer func() { cancel(); <-done }()
	var out bytes.Buffer
	if e = run([]string{"status", "-socket", socket}, &out, io.Discard); e != nil {
		t.Fatal(e)
	}
	var status session.Status
	if json.Unmarshal(out.Bytes(), &status) != nil || status.SourceState != "idle" || status.Active {
		t.Fatalf("bad status %s", out.String())
	}
	file := filepath.Join(dir, "profile.json")
	os.WriteFile(file, []byte(`{"version":1,"verified":false,"points":[]}`), 0600)
	out.Reset()
	if e = run([]string{"profile", "-socket", socket, "-device", "tv", "-file", file}, &out, io.Discard); e != nil {
		t.Fatal(e)
	}
	loaded, e := config.Load(path)
	if e != nil || loaded.Profiles["tv"].Version != 1 || loaded.Profiles["tv"].Verified {
		t.Fatal("CLI failed to preserve unverified profile")
	}
	if e = run([]string{"control", "-socket", socket, "-action", "stop"}, io.Discard, io.Discard); e != nil {
		t.Fatal(e)
	}
}
func TestOperatorCLIMalformedArguments(t *testing.T) {
	for _, args := range [][]string{{"serve"}, {"status"}, {"control", "-socket", "/missing", "-action", "target", "-target-db", "NaN"}, {"profile", "-socket", "/missing", "-file", "/missing"}} {
		if run(args, io.Discard, io.Discard) == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
