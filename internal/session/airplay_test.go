package session

import (
	"context"
	"errors"
	"testing"

	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/config"
	"github.com/khenrix/home-sound/internal/webos"
)

type nativeFixture struct {
	level   int
	lie     bool
	calls   []int
	updates chan webos.Volume
}

func (n *nativeFixture) SetVolume(ctx context.Context, v int) error {
	n.calls = append(n.calls, v)
	n.level = v
	return nil
}
func (n *nativeFixture) Volume(ctx context.Context) (webos.Volume, error) {
	if n.lie {
		return webos.Volume{Level: 10, Valid: true}, nil
	}
	return webos.Volume{Level: n.level, Valid: true}, nil
}
func (n *nativeFixture) WatchVolume(ctx context.Context) (<-chan webos.Volume, error) {
	return n.updates, nil
}
func TestTVRequestedNativeAppliedAndVerifiedBeforePCM(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := &sink{native: 12}
	native := &nativeFixture{updates: make(chan webos.Volume)}
	o := &nativeOutput{Output: base, native: native}
	if o.SetNative(ctx, 12) == nil {
		t.Fatal("native command accepted before prepare")
	}
	if e := o.Prepare(ctx, 7, 0); e != nil {
		t.Fatal(e)
	}
	if e := o.SetNative(ctx, 12); e != nil {
		t.Fatal(e)
	}
	// Only the real native boundary is replaced: verify its requested value and
	// readback, then exercise the audio-release guard on the wrapper itself.
	if len(native.calls) != 1 || native.calls[0] != 12 {
		t.Fatalf("wrong requested level %v", native.calls)
	}
	if e := o.Write(ctx, audio.Frame{Epoch: 7, Data: []float32{0, 0}}); e != nil {
		t.Fatal(e)
	}
	native.lie = true
	if e := o.SetNative(ctx, 13); e == nil {
		t.Fatal("mismatched readback accepted")
	}
	if e := o.Write(ctx, audio.Frame{Epoch: 7, Sample: 1, Data: []float32{0, 0}}); e == nil {
		t.Fatal("PCM released after failed native verification")
	}
	if e := o.SetNative(ctx, 16); e == nil {
		t.Fatal("TV ceiling bypassed")
	}
}
func TestRealFactoryLoadingDoesNotContactDevices(t *testing.T) {
	f := AirPlayFactory(testConfig())
	if f == nil {
		t.Fatal("no factory")
	}
	_, err := f(context.Background(), DeviceRequest{ID: "hall", Device: config.Device{Adapter: "sendspin"}})
	if !errors.Is(err, ErrUnsupportedOutput) {
		t.Fatalf("unimplemented adapter wasn't explicit: %v", err)
	}
}
