package capture

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func helper(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "arecord")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestCaptureErrors(t *testing.T) {
	for name, config := range map[string]Config{
		"missing helper": {Helper: "/missing/arecord", Device: "hw:CARD=PCH,DEV=0"},
		"missing device": {Helper: helper(t, "exit 2"), Device: ""},
		"nonzero exit":   {Helper: helper(t, "exit 2"), Device: "missing"},
		"truncated":      {Helper: helper(t, "printf '\\001'"), Device: "fixture"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := Record(context.Background(), config, time.Second)
			if err == nil {
				t.Fatal("expected capture failure")
			}
		})
	}
}
func TestDurationAndCancellation(t *testing.T) {
	cfg := Config{Helper: helper(t, "exec sleep 30"), Device: "fixture"}
	if _, _, err := Record(context.Background(), cfg, 61*time.Second); err == nil {
		t.Fatal("accepted duration above limit")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, _, err := Record(ctx, cfg, time.Second); err == nil {
		t.Fatal("expected cancellation")
	}
	if time.Since(start) > time.Second {
		t.Fatal("child was not stopped promptly")
	}
}

func TestRejectNonzeroExitAfterCompletePCM(t *testing.T) {
	cfg := Config{Helper: helper(t, "dd if=/dev/zero bs=1920 count=1 2>/dev/null\nexit 7"), Device: "fixture"}
	if _, _, err := Record(context.Background(), cfg, 10*time.Millisecond); err == nil {
		t.Fatal("accepted a failing helper")
	}
}

func TestRejectExcessPCM(t *testing.T) {
	cfg := Config{Helper: helper(t, "exec dd if=/dev/zero bs=4096 count=20 2>/dev/null"), Device: "fixture"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := Record(ctx, cfg, 10*time.Millisecond); err == nil {
		t.Fatal("accepted excess PCM")
	}
	if ctx.Err() != nil {
		t.Fatal("failed to stop helper after excess PCM")
	}
}
