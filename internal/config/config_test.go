package config

import (
	"encoding/json"
	"github.com/khenrix/home-sound/internal/volume"
	"os"
	"path/filepath"
	"testing"
)

func fixture() Config {
	return Config{Version: 1, Groups: map[string][]string{TVAndSpeakers: {"tv", "kitchen", "roam", "hall"}, AllSpeakers: {"kitchen", "roam", "hall"}}, Devices: map[string]Device{"tv": {Kind: "tv", Adapter: "airplay"}, "kitchen": {Kind: "speaker", Adapter: "airplay"}, "roam": {Kind: "speaker", Adapter: "airplay"}, "hall": {Kind: "speaker", Adapter: "sendspin"}}, Profiles: map[string]volume.Profile{}}
}
func TestExactGroupsAndUnverifiedPreview(t *testing.T) {
	c := fixture()
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	if c.Ready() {
		t.Fatal("missing profiles promoted to ready")
	}
	c.Groups[AllSpeakers] = append(c.Groups[AllSpeakers], "tv")
	if c.Validate() == nil {
		t.Fatal("TV leaked into speakers-only")
	}
	c = fixture()
	c.Groups["extra"] = []string{"tv"}
	if c.Validate() == nil {
		t.Fatal("third destination accepted")
	}
	c = fixture()
	c.Groups[TVAndSpeakers] = []string{"tv", "kitchen"}
	if c.Validate() == nil {
		t.Fatal("memberships disagree")
	}
}
func TestAtomicStorageKeepsPreviousOnInvalidAndProtectsFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(dir, "config.json")
	c := fixture()
	if e := Save(path, c); e != nil {
		t.Fatal(e)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	before, _ := os.ReadFile(path)
	c.Version = 9
	if Save(path, c) == nil {
		t.Fatal("invalid config saved")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed save damaged previous config")
	}
	c = fixture()
	c.Devices["tv"] = Device{Kind: "tv", Adapter: "airplay", RoomTrimDB: 2}
	if e := Save(path, c); e != nil {
		t.Fatal(e)
	}
	loaded, e := Load(path)
	if e != nil || loaded.Devices["tv"].RoomTrimDB != 2 {
		t.Fatalf("replacement not readable: %+v %v", loaded, e)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatal("temporary file leaked")
	}
	var raw map[string]any
	json.Unmarshal(after, &raw)
	raw["credentials"] = "must-not-be-accepted"
	bad, _ := json.Marshal(raw)
	os.WriteFile(path, bad, 0600)
	if _, e := Load(path); e == nil {
		t.Fatal("unknown secret field accepted")
	}
}
func TestStorageRefusesSymlinkAndPublicDirectory(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0755)
	if Save(filepath.Join(dir, "config.json"), fixture()) == nil {
		t.Fatal("public directory accepted")
	}
	os.Chmod(dir, 0700)
	real := filepath.Join(dir, "real")
	os.WriteFile(real, []byte("preserve"), 0600)
	os.Symlink(real, filepath.Join(dir, "link"))
	if Save(filepath.Join(dir, "link"), fixture()) == nil {
		t.Fatal("symlink accepted")
	}
	b, _ := os.ReadFile(real)
	if string(b) != "preserve" {
		t.Fatal("symlink target changed")
	}
}
