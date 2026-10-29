package main

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/khenrix/home-sound/internal/wave"
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

func TestProbeAndAnalyzeCLI(t *testing.T) {
	dir := t.TempDir()
	ref := filepath.Join(dir, "probe.wav")
	rec := filepath.Join(dir, "recorded.wav")
	if err := run([]string{"signal", "-kind", "probe", "-duration", "1s", "-output", ref}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(ref)
	if err != nil {
		t.Fatal(err)
	}
	format, samples, err := wave.Read(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	shifted := make([]float32, len(samples)+960)
	for i, v := range samples {
		shifted[i+960] = v * 0.5
	}
	f, err = os.Create(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err = wave.Write(f, format, shifted); err != nil {
		t.Fatal(err)
	}
	f.Close()
	var out bytes.Buffer
	if err = run([]string{"analyze", "-reference", ref, "-recording", rec}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var report struct {
		LagSamples             int     `json:"lag_samples"`
		RelativeDB             float64 `json:"relative_level_db"`
		AbsoluteSPL            string  `json:"absolute_spl"`
		Quality                string  `json:"quality"`
		CalibrationEligibility string  `json:"calibration_eligibility"`
	}
	if err = json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.LagSamples != 480 || math.Abs(report.RelativeDB+6.0206) > 0.05 || report.AbsoluteSPL != "unverified" || report.Quality != "heuristic" || report.CalibrationEligibility != "not_acoustically_verified" {
		t.Fatalf("bad analysis: %+v", report)
	}
}

func TestRecordCLI(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper")
	path := filepath.Join(dir, "recording.wav")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexec dd if=/dev/zero bs=1920 count=1 2>/dev/null\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"record", "-helper", helper, "-device", "fixture", "-duration", "10ms", "-output", path}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, samples, err := wave.Read(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 960 {
		t.Fatalf("got %d samples", len(samples))
	}
}

func TestRecordStopsOnTermination(t *testing.T) {
	if os.Getenv("HOME_SOUND_SIGNAL_TEST") == "1" {
		_ = run([]string{"record", "-helper", os.Getenv("HOME_SOUND_HELPER"), "-device", "fixture", "-output", os.Getenv("HOME_SOUND_OUTPUT")}, io.Discard, io.Discard)
		return
	}
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper")
	pidfile := filepath.Join(dir, "pid")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\necho $$ > '"+pidfile+"'\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecordStopsOnTermination$")
	cmd.Env = append(os.Environ(), "HOME_SOUND_SIGNAL_TEST=1", "HOME_SOUND_HELPER="+helper, "HOME_SOUND_OUTPUT="+filepath.Join(dir, "out.wav"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waited := false
	defer func() {
		_ = cmd.Process.Kill()
		if !waited {
			<-done
		}
	}()
	var pid int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(pidfile)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("helper did not start")
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		waited = true
	case <-time.After(5 * time.Second):
		t.Fatal("CLI did not stop")
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatal("capture child survived termination")
	}
}
