package airplay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/khenrix/home-sound/internal/audio"
	"github.com/khenrix/home-sound/internal/webos"
)

// A live WebSocket alone must not keep playback enabled when native volume
// requests fail. Only the event case sends another subscription update; it must
// stop output while an unanswered periodic request is still pending.
func TestNativeLGPeriodicReadbackFailure(t *testing.T) {
	for _, mode := range []string{"wedged", "invalid", "muted", "above_limit", "safe_then_wedged", "event_during_readback"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			readback := make(chan struct{}, 1)
			closed := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(closed)
				c, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer c.CloseNow()
				subscribed := false
				reads, sets, probes := 0, 0, 0
				var subscriptionID any
				for {
					var m map[string]any
					if wsjson.Read(ctx, c, &m) != nil {
						return
					}
					typ := "response"
					payload := map[string]any{"returnValue": true}
					switch m["type"] {
					case "register":
						typ = "registered"
						payload = map[string]any{"client-key": "test-local-key"}
					default:
						switch m["uri"] {
						case "ssap://audio/setVolume":
							sets++
							if subscribed || m["payload"].(map[string]any)["volume"] != float64(10) {
								t.Error("unexpected native volume setter")
							}
						case "ssap://audio/getVolume":
							payload["volume"] = 10
							payload["muted"] = false
							if m["type"] == "subscribe" {
								if reads != 2 || sets != 2 {
									t.Error("subscription preceded both native gates")
								}
								subscribed = true
								subscriptionID = m["id"]
							} else if !subscribed {
								reads++
							} else {
								probes++
								if mode != "safe_then_wedged" || probes > 1 {
									select {
									case readback <- struct{}{}:
									default:
									}
								}
								switch mode {
								case "safe_then_wedged":
									if probes == 1 {
										payload["volume"] = 15
									} else {
										continue
									}
								case "event_during_readback":
									if wsjson.Write(ctx, c, map[string]any{"id": subscriptionID, "type": "response", "payload": map[string]any{"returnValue": true, "volume": 16, "muted": false}}) != nil {
										return
									}
									continue
								case "wedged":
									continue // Keep servicing the transport, but never answer volume requests.
								case "invalid":
									delete(payload, "muted")
								case "muted":
									payload["muted"] = true
								case "above_limit":
									payload["volume"] = 16
								}
							}
						default:
							t.Error("unexpected native request")
							return
						}
					}
					if wsjson.Write(ctx, c, map[string]any{"id": m["id"], "type": typ, "payload": payload}) != nil {
						return
					}
				}
			}))
			defer srv.Close()
			c, err := webos.Connect(ctx, webos.Config{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), PairingFile: filepath.Join(t.TempDir(), "lg.json")}, true)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			helperMode := "ready"
			if mode == "wedged" {
				helperMode = "ntp_cold"
			}
			cfg := fixture(t, helperMode)
			cfg.InitialVolume = 0
			cfg.LG = c
			o, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer o.Stop()
			if err = o.Prepare(ctx, 3, 0); err != nil {
				t.Fatal(err)
			}
			if s := o.Status(); !s.Healthy || !s.Ready {
				t.Fatal("fixture never reached a valid initial subscription")
			}
			if err = o.Write(ctx, audio.Frame{Epoch: 3, Data: []float32{.1, .1}}); err != nil {
				t.Fatal(err)
			}
			failureBound := 3500 * time.Millisecond
			if mode == "event_during_readback" {
				select {
				case <-readback:
				case <-time.After(2 * time.Second):
					t.Fatal("no native readback request")
				}
				failureBound = 500 * time.Millisecond
			}
			select {
			case <-o.done:
			case <-time.After(failureBound):
				t.Fatalf("native %s control left helper running: %+v", mode, o.Status())
			}
			if mode != "event_during_readback" {
				select {
				case <-readback:
				default:
					t.Fatal("adapter stopped without a periodic native volume request")
				}
			}
			select {
			case <-closed:
				t.Fatal("fixture connection closed before liveness failure was established")
			default:
			}
			if s := o.Status(); s.Healthy || s.Ready {
				t.Fatalf("failed native control retained readiness: %+v", s)
			}
			if err = o.Write(ctx, audio.Frame{Epoch: 3, Sample: 1, Data: []float32{.1, .1}}); err == nil {
				t.Fatal("PCM accepted after native control failure")
			}
			if _, err = o.Start(ctx, 3, time.Now().Add(time.Second)); err == nil {
				t.Fatal("START accepted after native control failure")
			}
		})
	}
}
