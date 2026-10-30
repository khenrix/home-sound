package inputs

import (
	"github.com/khenrix/home-sound/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestDisabledInputsHaveNoSideEffects(t *testing.T) {
	c := Config{Version: 1}
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	ids, e := Prepare(c)
	if e != nil || len(ids) != 0 {
		t.Fatalf("disabled inputs touched runtime: %v %v", ids, e)
	}
}
func TestPrivateStableTwoIdentities(t *testing.T) {
	c := Config{Version: 1, StateDir: filepath.Join(t.TempDir(), "private"), BindIP: "127.0.0.1", YouTube: YouTubeConfig{Enabled: true, Mode: "bytes", Ports: [2]int{38801, 38802}}, Volume: VolumeConfig{MinDB: -50, MaxDB: -30, Initial: 20}}
	ids, e := Prepare(c)
	if e != nil {
		t.Fatal(e)
	}
	again, e := Prepare(c)
	if e != nil {
		t.Fatal(e)
	}
	if len(ids) != 2 || ids[0].Group != config.TVAndSpeakers || ids[1].Group != config.AllSpeakers || ids[0].DeviceID == ids[1].DeviceID || ids[0].UUID == ids[1].UUID || ids[0] != again[0] {
		t.Fatalf("wrong identities: %+v", ids)
	}
	st, _ := os.Stat(c.StateDir)
	if st.Mode().Perm() != 0700 {
		t.Fatal("state not private")
	}
	st, _ = os.Stat(filepath.Join(c.StateDir, "identities.json"))
	if st.Mode().Perm() != 0600 {
		t.Fatal("IDs not private")
	}
	os.Chmod(c.StateDir, 0755)
	if _, e := Prepare(c); e == nil {
		t.Fatal("accepted unprotected directory")
	}
}

func TestPrivateStateRejectsSymlinkAncestors(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	os.Mkdir(real, 0700)
	alias := filepath.Join(root, "alias")
	os.Symlink(real, alias)
	if e := PrivateDir(filepath.Join(alias, "state")); e == nil {
		t.Fatal("private state followed a symlink ancestor")
	}
	if _, e := os.Stat(filepath.Join(real, "state")); !os.IsNotExist(e) {
		t.Fatal("rejected path still wrote state")
	}
}
