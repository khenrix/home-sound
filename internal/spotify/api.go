package spotify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type API struct{ Origin string }

func (a *API) endpoint(path string) (string, error) {
	u, e := url.Parse(a.Origin)
	if e != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid helper origin")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return "", errors.New("helper API must be loopback")
	}
	return a.Origin + path, nil
}
func (a *API) request(ctx context.Context, method, path string, v any) ([]byte, error) {
	endpoint, e := a.endpoint(path)
	if e != nil {
		return nil, e
	}
	var b []byte
	if v != nil {
		b, e = json.Marshal(v)
		if e != nil {
			return nil, e
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	r, e := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(b))
	if e != nil {
		return nil, errors.New("invalid helper request")
	}
	r.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, e := client.Do(r)
	if e != nil {
		return nil, errors.New("helper API unavailable")
	}
	defer res.Body.Close()
	b, e = io.ReadAll(io.LimitReader(res.Body, 16385))
	if e != nil || len(b) > 16384 || res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, errors.New("helper API rejected request")
	}
	return b, nil
}
func (a *API) Volume(ctx context.Context, n int) error {
	if n < 0 || n > 100 {
		return errors.New("invalid external volume")
	}
	_, e := a.request(ctx, "POST", "/player/volume", map[string]any{"volume": n, "relative": false})
	return e
}
func (a *API) CurrentVolume(ctx context.Context) (int, error) {
	b, e := a.request(ctx, "GET", "/player/volume", nil)
	var v struct{ Value, Max int }
	if e != nil || json.Unmarshal(b, &v) != nil || v.Max <= 0 || v.Value < 0 || v.Value > v.Max {
		return 0, errors.New("helper volume unavailable")
	}
	return (v.Value*100 + v.Max/2) / v.Max, nil
}
func (a *API) Control(ctx context.Context, action string, position time.Duration) error {
	path := ""
	var payload any
	switch action {
	case "pause", "resume", "next", "stop":
		path = "/player/" + action
	case "previous":
		path = "/player/prev"
	case "seek":
		if position < 0 || position > 6*time.Hour {
			return errors.New("invalid seek")
		}
		path = "/player/seek"
		payload = map[string]any{"position": position.Milliseconds(), "relative": false}
	default:
		return errors.New("unsupported helper control")
	}
	_, e := a.request(ctx, "POST", path, payload)
	return e
}

// Watch drains bounded events continuously. Values in the asynchronous event
// payload never establish PCM identity: consumers reconcile current REST state.
func (a *API) Watch(ctx context.Context, notify func(string)) {
	endpoint, e := a.endpoint("/events")
	if e != nil {
		return
	}
	endpoint = "ws" + strings.TrimPrefix(endpoint, "http")
	for ctx.Err() == nil {
		c, _, e := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: &http.Client{Timeout: 2 * time.Second}})
		if e == nil {
			c.SetReadLimit(16384)
			notify("reconcile")
			for {
				_, b, re := c.Read(ctx)
				if re != nil {
					break
				}
				var v struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(b, &v) != nil || len(v.Type) > 64 {
					break
				}
				switch v.Type {
				case "active", "inactive", "volume", "stopped":
					notify(v.Type)
				}
			}
			c.CloseNow()
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
