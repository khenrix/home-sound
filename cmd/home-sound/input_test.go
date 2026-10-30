package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/inputs"
	"github.com/khenrix/home-sound/internal/source"
	"github.com/khenrix/home-sound/internal/volume"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInputProbeExplicitEnableAndNoSpeakerProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inputs.json")
	os.WriteFile(path, []byte(`{"version":1}`), 0600)
	var out bytes.Buffer
	if e := run([]string{"input-probe", "-config", path, "-duration", "20ms"}, &out, io.Discard); e == nil {
		t.Fatal("implicit input startup allowed")
	}
	if e := run([]string{"input-probe", "-enable-inputs", "-config", path, "-duration", "20ms"}, &out, io.Discard); e != nil {
		t.Fatal(e)
	}
	var v map[string]any
	if json.Unmarshal(out.Bytes(), &v) != nil || v["audible"] != false || v["native_accepted"] != false {
		t.Fatalf("probe claimed speaker or native acceptance: %s", out.String())
	}
}

func TestServiceReportsOwnedInputProcessFailure(t *testing.T) {
	root := t.TempDir()
	port := func() int {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	ports := [2]int{port(), port()}
	if ports[0] == ports[1] {
		ports[1] = port()
	}
	helper := filepath.Join(root, "fixture-helper")
	script := []byte("#!/usr/bin/python3\nimport http.server,os,re,sys,threading\ntext=open(os.path.join(sys.argv[2],'config.yml')).read()\nport=int(re.search(r'port: ([0-9]+)',text).group(1))\nclass Handler(http.server.BaseHTTPRequestHandler):\n def do_POST(self):\n  self.send_response(204);self.end_headers()\n def log_message(self,*args): pass\nserver=http.server.HTTPServer(('127.0.0.1',port),Handler)\nthreading.Timer(0.7,lambda:os._exit(9)).start()\nserver.serve_forever()\n")
	os.WriteFile(helper, script, 0700)
	sum := sha256.Sum256(script)
	cfg := inputs.Config{Version: 1, StateDir: filepath.Join(root, "native"), Spotify: inputs.SpotifyConfig{Enabled: true, Ports: ports, Helper: source.Helper{Path: helper, SHA256: hex.EncodeToString(sum[:])}}, Volume: inputs.VolumeConfig{MinDB: -50, MaxDB: -30, Initial: 20}}
	b, _ := json.Marshal(cfg)
	inputPath := filepath.Join(root, "inputs.json")
	os.WriteFile(inputPath, b, 0600)
	speakerPath := filepath.Join(root, "operator", "config.json")
	c := config.Config{Version: 1, Groups: map[string][]string{config.TVAndSpeakers: {"tv", "kitchen", "roam", "hall"}, config.AllSpeakers: {"kitchen", "roam", "hall"}}, Devices: map[string]config.Device{"tv": {Kind: "tv", Adapter: "airplay"}, "kitchen": {Kind: "speaker", Adapter: "airplay"}, "roam": {Kind: "speaker", Adapter: "airplay"}, "hall": {Kind: "speaker", Adapter: "sendspin"}}, Profiles: map[string]volume.Profile{}}
	if e := config.Save(speakerPath, c); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		done <- run([]string{"serve", "-socket", filepath.Join(root, "operator", "api.sock"), "-config", speakerPath, "-enable-inputs", "-inputs-config", inputPath}, io.Discard, io.Discard)
	}()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("owned input process failure reported successful service exit")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owned process failure did not stop service")
	}
}
