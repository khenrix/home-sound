package airplay

import (
	"context"
	"io"
	"os"
	"testing"
)

func TestNativeSpeakerVolumeWritesPinnedHelperCommand(t *testing.T) {
	r, w, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	defer w.Close()
	a := &Adapter{commands: w, status: Status{Healthy: true, Ready: true}}
	if e = a.SetNative(context.Background(), 12); e != nil {
		t.Fatal(e)
	}
	got := make([]byte, 10)
	if _, e = io.ReadFull(r, got); e != nil {
		t.Fatal(e)
	}
	if string(got) != "VOLUME=12\n" {
		t.Fatalf("wrong command %q", got)
	}
	if err := a.SetNative(context.Background(), 60); err != nil {
		t.Fatal(err)
	}
	higher := make([]byte, 10)
	if _, err := io.ReadFull(r, higher); err != nil {
		t.Fatal(err)
	}
	if string(higher) != "VOLUME=60\n" {
		t.Fatalf("wrong measured-range control %q", higher)
	}
	if a.SetNative(context.Background(), 101) == nil {
		t.Fatal("invalid native upper range accepted")
	}
	a.cfg.LG = &volumeFixture{}
	if a.SetNative(context.Background(), 12) == nil {
		t.Fatal("LG AirPlay setter was enabled")
	}
	a.cfg.LG = nil
	a.status.Ready = false
	if a.SetNative(context.Background(), 10) == nil {
		t.Fatal("cold output accepted volume")
	}
}
