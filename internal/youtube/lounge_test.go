package youtube

import (
	"context"
	"fmt"
	"github.com/khenrix/home-sound/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func loungeBatch(w http.ResponseWriter, s string) { fmt.Fprintf(w, "%d\n%s", len(s), s) }
func TestLoungeHandshakeSendEOFReconnectAndDuplicateCursor(t *testing.T) {
	var polls atomic.Int32
	sent := make(chan bool, 1)
	paired := make(chan bool, 1)
	secondPoll := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.URL.Path {
		case "/pairing/generate_screen_id":
			fmt.Fprint(w, "fixture-screen")
		case "/pairing/get_lounge_token_batch":
			if r.PostForm.Get("screen_ids") != "fixture-screen" {
				t.Error("wrong screen")
			}
			fmt.Fprint(w, `{"screens":[{"screenId":"fixture-screen","loungeToken":"fixture-token"}]}`)
		case "/pairing/register_pairing_code":
			paired <- r.PostForm.Get("pairing_code") == "fixture-code" && r.PostForm.Get("access_type") == "permanent" && r.PostForm.Get("screen_name") == config.AllSpeakers
			fmt.Fprint(w, "{}")
		case "/bc/bind":
			q := r.URL.Query()
			if r.Method == "POST" && r.PostForm.Get("count") == "0" {
				if q.Get("RID") == "rpc" || q.Get("loungeIdToken") != "fixture-token" {
					t.Error("invalid init")
				}
				loungeBatch(w, `[[0,["c","sid"]],[1,["S","gs"]]]`)
			} else if r.Method == "POST" {
				sent <- r.PostForm.Get("req0__sc") == "onVolumeChanged" && r.PostForm.Get("req0_volume") == "37" && r.PostForm.Get("ofs") == "0" && (q.Get("AID") == "1" || q.Get("AID") == "2")
				fmt.Fprint(w, "{}")
			} else {
				if q.Get("RID") != "rpc" || q.Get("SID") != "sid" || q.Get("gsessionid") != "gs" || q.Get("TYPE") != "xmlhttp" {
					t.Error("invalid poll")
				}
				if polls.Add(1) == 1 {
					loungeBatch(w, `[[2,["play",{}]],[2,["pause",{}]]]`)
				} else {
					if q.Get("AID") != "2" {
						t.Errorf("wrong cursor %s", q.Get("AID"))
					}
					select {
					case secondPoll <- struct{}{}:
					default:
					}
					<-r.Context().Done()
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	l, e := NewLounge(LoungeOptions{Name: config.AllSpeakers, DeviceID: "fixture-device", Mode: "bytes", Endpoint: srv.URL, Client: srv.Client()})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	messages := make(chan string, 4)
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx, "fixture-code", func(m Message) error { messages <- m.Name; return nil }) }()
	select {
	case <-l.Ready():
	case e := <-done:
		t.Fatalf("Lounge start: %v", e)
	case <-ctx.Done():
		t.Fatal("handshake timed out")
	}
	if !<-paired {
		t.Fatal("pairing registration fields")
	}
	if e = l.Send(ctx, "onVolumeChanged", map[string]string{"volume": "37"}); e != nil {
		t.Fatal(e)
	}
	if !<-sent {
		t.Fatal("send RID/ofs/_sc fields")
	}
	select {
	case <-secondPoll:
	case <-ctx.Done():
		t.Fatal("EOF did not reconnect")
	}
	cancel()
	<-done
	if len(messages) != 1 || <-messages != "play" {
		t.Fatal("duplicate cursor dispatched twice")
	}
}
func TestLoungeUnknownSIDAndExpiredTokenRebind(t *testing.T) {
	for _, status := range []int{400, 410} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var token, init atomic.Int32
			rebound := make(chan bool, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.ParseForm()
				switch {
				case strings.HasSuffix(r.URL.Path, "get_lounge_token_batch"):
					token.Add(1)
					fmt.Fprint(w, `{"screens":[{"screenId":"screen","loungeToken":"fixture"}]}`)
				case strings.HasSuffix(r.URL.Path, "register_pairing_code"):
					fmt.Fprint(w, "{}")
				case r.Method == "POST":
					n := init.Add(1)
					if n == 2 {
						q := r.URL.Query()
						rebound <- status == 400 && q.Get("OSID") == "sid" && q.Get("OAID") == "1" || status == 410 && token.Load() == 2
					}
					loungeBatch(w, `[[0,["c","sid"]],[1,["S","gs"]]]`)
				default:
					if init.Load() == 1 {
						w.WriteHeader(status)
						fmt.Fprint(w, "Unknown SID")
					} else {
						<-r.Context().Done()
					}
				}
			}))
			defer srv.Close()
			l, _ := NewLounge(LoungeOptions{Name: config.AllSpeakers, DeviceID: "device", ScreenID: "screen", Mode: "bytes", Endpoint: srv.URL, Client: srv.Client()})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- l.Run(ctx, "code", func(Message) error { return nil }) }()
			select {
			case ok := <-rebound:
				if !ok {
					t.Fatal("wrong rebind parameters")
				}
			case e := <-done:
				t.Fatalf("reconnect ended: %v", e)
			case <-ctx.Done():
				t.Fatal("rebind missing")
			}
			cancel()
			<-done
		})
	}
}
