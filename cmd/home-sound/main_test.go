package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"unknown"}, {"signal"}, {"signal", "-duration", "1s"}, {"signal", "-output", "x.wav"}, {"signal", "-duration", "-1s", "-output", "x.wav"}, {"inspect"}, {"inspect", "/missing/file.wav"}} {
		var out, errOut bytes.Buffer
		if run(args, &out, &errOut) == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
func TestSignalAndInspect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signal.wav")
	var out, errOut bytes.Buffer
	if err := run([]string{"signal", "-duration", "10ms", "-output", path}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"inspect", path}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"48000", "2", "10ms"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("inspect output %q missing %q", out.String(), want)
		}
	}
}
