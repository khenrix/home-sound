package youtube

import (
	"context"
	"fmt"
	"github.com/khenrix/home-sound/internal/source"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeAppLaunchOutlivesRequestAndRestart(t *testing.T) {
	var binds atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.URL.Path {
		case "/pairing/get_lounge_token_batch":
			fmt.Fprint(w, `{"screens":[{"screenId":"screen","loungeToken":"fixture"}]}`)
		case "/pairing/register_pairing_code":
			fmt.Fprint(w, `{}`)
		case "/bc/bind":
			if r.Method == "POST" {
				if r.PostForm.Get("count") == "0" {
					binds.Add(1)
					loungeBatch(w, `[[0,["c","sid"]],[1,["S","gs"]]]`)
				} else {
					fmt.Fprint(w, `{}`)
				}
			} else if r.URL.Query().Get("AID") == "1" {
				loungeBatch(w, `[[2,["setPlaylist",{"videoIds":"abcdefghijk"}]]]`)
			} else {
				<-r.Context().Done()
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	o := source.NewObserver()
	defer o.Stop()
	co := source.NewCoordinator(o, nil)
	var instances atomic.Int32
	a := &NativeApp{Parent: ctx, Factory: func() (*Lounge, *PlaybackController, error) {
		l, e := NewLounge(LoungeOptions{Name: "All Speakers", DeviceID: "device", ScreenID: "screen", Mode: "bytes", Endpoint: srv.URL})
		r := &resolveFixture{make(chan string, 8), make(chan struct{})}
		close(r.wait)
		return l, NewPlaybackController("All Speakers", fmt.Sprintf("%032x", instances.Add(1)), co, r, decodeFixture{}, -30), e
	}}
	defer a.Stop()
	for round := uint64(1); round <= 2; round++ {
		request, stop := context.WithCancel(ctx)
		if e := a.Launch(request, "fixture-code"); e != nil {
			t.Fatal(e)
		}
		stop()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) && (o.Snapshot().Epoch < round || o.Snapshot().Frames < 960) {
			time.Sleep(time.Millisecond)
		}
		if !a.Running() || o.Snapshot().Epoch != round || o.Snapshot().Frames < 960 {
			t.Fatalf("launch request owned session lifetime: %+v", o.Snapshot())
		}
		if e := a.Stop(); e != nil {
			t.Fatal(e)
		}
		if a.Running() {
			t.Fatal("stop left Lounge running")
		}
	}
	if binds.Load() != 2 {
		t.Fatalf("restart reused failed transient session: %d", binds.Load())
	}
}
