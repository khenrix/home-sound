package webos

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/khenrix/home-sound/internal/pairing"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercises actual registration, correlated replies and volume subscription over TCP.
func TestRegistrationVolumeAndSubscription(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer c.CloseNow()
		for {
			var m map[string]any
			if wsjson.Read(ctx, c, &m) != nil {
				return
			}
			id := m["id"]
			typ := m["type"]
			p := map[string]any{"returnValue": true}
			if typ == "register" {
				pl := m["payload"].(map[string]any)
				if pl["client-key"] != "test-key" {
					t.Error("stored key not supplied")
				}
				typ = "registered"
				p = map[string]any{"client-key": "test-key"}
			} else {
				typ = "response"
				switch m["uri"] {
				case "ssap://audio/setVolume":
					if m["payload"].(map[string]any)["volume"] != float64(10) {
						t.Error("wrong level")
					}
				case "ssap://audio/getVolume":
					p["volumeStatus"] = map[string]any{"volume": 10, "muteStatus": false}
				default:
					t.Error("unexpected URI")
				}
			}
			wsjson.Write(ctx, c, map[string]any{"id": id, "type": typ, "payload": p})
			if m["type"] == "subscribe" {
				wsjson.Write(ctx, c, map[string]any{"id": id, "type": "response", "payload": map[string]any{"returnValue": true, "volume": 16, "muted": false}})
			}
		}
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "lg.json")
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	pairing.Save(path, credentials{Endpoint: url, ClientKey: "test-key"})
	c, err := Connect(ctx, Config{URL: url, PairingFile: path}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.SetVolume(ctx, 10); err != nil {
		t.Fatal(err)
	}
	v, err := c.Volume(ctx)
	if err != nil || v.Level != 10 || v.Muted {
		t.Fatalf("bad status: %+v %v", v, err)
	}
	ch, err := c.WatchVolume(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v := <-ch; v.Level != 10 || !v.Valid {
		t.Fatalf("bad first update: %+v", v)
	}
	if v := <-ch; v.Level != 16 || !v.Valid {
		t.Fatalf("lost unsafe update: %+v", v)
	}
}
func TestRejectUnknownVolume(t *testing.T) {
	for _, s := range []string{`{}`, `{"volume":10}`, `{"volume":101,"muted":false}`, `{"returnValue":false,"volume":10,"muted":false}`} {
		if _, err := decodeVolume(json.RawMessage(s)); err == nil {
			t.Fatal("accepted missing/invalid state")
		}
	}
}

func TestPinnedTLSRegistration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer c.CloseNow()
		var m map[string]any
		if wsjson.Read(ctx, c, &m) != nil {
			return
		}
		wsjson.Write(ctx, c, map[string]any{"type": "registered", "id": m["id"], "payload": map[string]any{"client-key": "private-test-key"}})
		c.Read(ctx)
	}))
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	cfg := Config{URL: "wss" + strings.TrimPrefix(srv.URL, "https"), PairingFile: filepath.Join(t.TempDir(), "lg.json"), CertificateSHA256: hex.EncodeToString(sum[:])}
	c, e := Connect(ctx, cfg, true)
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	cfg.CertificateSHA256 = strings.Repeat("0", 64)
	if c, e = Connect(ctx, cfg, false); e == nil {
		c.Close()
		t.Fatal("accepted wrong server certificate")
	}
}
