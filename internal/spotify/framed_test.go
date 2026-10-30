package spotify

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/source"
	"io"
	"net"
	"testing"
	"time"
)

func wire(c net.Conn, g uint64, off uint64, pcm []byte, transport string) error {
	var credit [1]byte
	if _, e := io.ReadFull(c, credit[:]); e != nil {
		return e
	}
	b, _ := json.Marshal(map[string]any{"Version": 1, "Nonce": "11111111111111111111111111111111", "Generation": g, "Offset": off, "Identity": map[string]string{"uri": "spotify:track:fixture", "title": "track", "artist": "artist", "album": "album"}, "Reason": "source", "Rate": 44100, "Channels": 2, "PCM": pcm, "Terminal": "", "Transport": transport})
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	if _, e := c.Write(h[:]); e != nil {
		return e
	}
	_, e := c.Write(b)
	return e
}
func TestFramedGenerationPauseAndReconnectFence(t *testing.T) {
	o := source.NewObserver()
	defer o.Stop()
	co := source.NewCoordinator(o, nil)
	a, b := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer b.Close()
	leases := make(chan source.Lease, 4)
	done := make(chan error, 1)
	go func() {
		done <- (&Input{Group: config.AllSpeakers, Coordinator: co, Lease: func(l source.Lease) { leases <- l }}).Run(ctx, a)
	}()
	sent := make(chan error, 1)
	go func() {
		pcm := make([]byte, 882*8)
		for i := 0; i < len(pcm); i += 4 {
			binary.LittleEndian.PutUint32(pcm[i:], 0x3e800000)
		}
		if e := wire(b, 1, 0, pcm, "playing"); e != nil {
			sent <- e
			return
		}
		sent <- wire(b, 2, 0, nil, "paused")
	}()
	select {
	case e := <-done:
		t.Fatalf("framed source ended: %v", e)
	case <-time.After(time.Second):
		t.Fatal("no admitted generation")
	case l := <-leases:
		if l.Generation != 1 {
			t.Fatal(l)
		}
	}
	select {
	case l := <-leases:
		if l.Generation != 2 || !co.Current(l) {
			t.Fatal(l)
		}
	case e := <-done:
		t.Fatal(e)
	case <-time.After(time.Second):
		t.Fatal("pause/status starved by PCM backpressure")
	}
	if e := <-sent; e != nil {
		t.Fatal(e)
	}
	if o.Snapshot().Transport != "paused" {
		t.Fatal(o.Snapshot())
	}
	b.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disconnect did not cancel")
	}
}
