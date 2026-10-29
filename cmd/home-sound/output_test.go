package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/wave"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryInventoryCommand(t *testing.T) {
	p := filepath.Join(t.TempDir(), "inventory.json")
	os.WriteFile(p, []byte(`[{"name":"TV","kind":"lg","host":"192.0.2.1","port":3000}]`), 0600)
	var out, errs bytes.Buffer
	if err := run([]string{"discover", "-inventory", p}, &out, &errs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"source":"inventory"`) {
		t.Fatal("inventory not emitted")
	}
}
func TestOutputCommandsRejectUnsafeInputBeforeDeviceAccess(t *testing.T) {
	for _, args := range [][]string{{"play", "-duration", "31s"}, {"play", "-target", "lg", "-volume", "16"}, {"lg-volume", "-volume", "16"}, {"play", "-target", "speaker", "-volume", "0"}, {"airplay-status", "-target", "unknown"}, {"lg-status", "-url", "http://127.0.0.1"}} {
		var out, errs bytes.Buffer
		if err := run(args, &out, &errs); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestCLIHelperProcess(t *testing.T) {
	if os.Getenv("HOME_SOUND_CLI_FIXTURE") == "" {
		return
	}
	get := func(k string) string {
		for i, a := range os.Args {
			if a == k && i+1 < len(os.Args) {
				return os.Args[i+1]
			}
		}
		return ""
	}
	f, e := os.OpenFile(get("--cmdpipe"), os.O_RDWR, 0600)
	if e != nil {
		os.Exit(2)
	}
	defer f.Close()
	fmt.Fprintln(os.Stderr, "[STATUS] connected\n[STATUS] clock_ready mode=ntp state=ready streak_ms=0 exchanges=1 ready_in_ms=0 ready_at_unix_ms=0")
	go io.Copy(io.Discard, os.Stdin)
	s := bufio.NewScanner(f)
	requested := ""
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "START_UNIX_MS=") {
			requested = strings.TrimPrefix(line, "START_UNIX_MS=")
		}
		if line == "ACTION=START" {
			fmt.Fprintf(os.Stderr, "[STATUS] started requested_unix_ms=%s at_unix_ms=%d\n", requested, time.Now().Add(10*time.Millisecond).UnixMilli())
		}
		if line == "ACTION=STOP" {
			os.Exit(0)
		}
	}
	os.Exit(0)
}
func TestBoundedSpeakerProofCommand(t *testing.T) {
	d := t.TempDir()
	exe, _ := os.Executable()
	helper := filepath.Join(d, "helper")
	raw := []byte("#!/bin/sh\nGORACE=atexit_sleep_ms=0 HOME_SOUND_CLI_FIXTURE=1 exec '" + strings.ReplaceAll(exe, "'", "'\\''") + "' -test.run=TestCLIHelperProcess -- \"$@\"\n")
	os.WriteFile(helper, raw, 0700)
	sum := sha256.Sum256(raw)
	input := filepath.Join(d, "input.wav")
	f, e := os.Create(input)
	if e != nil {
		t.Fatal(e)
	}
	wave.Write(f, audio.Canonical, make([]float32, 960))
	f.Close()
	var out, errs bytes.Buffer
	e = run([]string{"play", "-helper", helper, "-helper-sha256", hex.EncodeToString(sum[:]), "-host", "127.0.0.1", "-target", "speaker", "-volume", "5", "-input", input, "-duration", "10ms"}, &out, &errs)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.String(), `"Frames":480`) || !strings.Contains(out.String(), `"Epoch":1`) {
		t.Fatalf("missing proof evidence: %s", out.String())
	}
}
