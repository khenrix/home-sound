package pairing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Catches disclosure through file modes, unsafe replacement, or symlink following.
func TestProtectedRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "private", "device.json")
	want := map[string]string{"credential": "synthetic-private-value", "dacp": "1234"}
	if err := Save(p, want); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := Load(p, &got); err != nil || got["credential"] != want["credential"] {
		t.Fatalf("round trip failed: %v", err)
	}
	fi, _ := os.Stat(p)
	di, _ := os.Stat(filepath.Dir(p))
	if fi.Mode().Perm() != 0600 || di.Mode().Perm() != 0700 {
		t.Fatal("unprotected pairing")
	}
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	if Load(p, &got) == nil || Save(p, want) == nil {
		t.Fatal("accepted permissive existing file")
	}
}
func TestRejectSymlink(t *testing.T) {
	d := t.TempDir()
	target := filepath.Join(d, "target")
	os.WriteFile(target, []byte(`{}`), 0600)
	p := filepath.Join(d, "link")
	os.Symlink(target, p)
	var v any
	if Load(p, &v) == nil || Save(p, map[string]string{}) == nil {
		t.Fatal("followed symlink")
	}
}

func TestRejectOversizedFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pair.json")
	os.WriteFile(p, []byte(`{}`+strings.Repeat(" ", 20000)), 0600)
	var v any
	if Load(p, &v) == nil {
		t.Fatal("accepted oversized pairing file")
	}
}
