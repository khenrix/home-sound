package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Message struct {
	ID     int64
	Name   string
	Values map[string]string
}
type LoungeOptions struct {
	Name, DeviceID, ScreenID, Mode, Endpoint string
	Client                                   *http.Client
	SaveScreen                               func(string) error
}
type Lounge struct {
	mu, send       sync.Mutex
	opts           LoungeOptions
	ready          chan struct{}
	readyOnce      sync.Once
	running        bool
	token, sid, gs string
	aid, rid, ofs  int64
}
type loungeHTTPError struct {
	status  int
	unknown bool
}

func (e loungeHTTPError) Error() string { return "Lounge request rejected" }
func NewLounge(o LoungeOptions) (*Lounge, error) {
	if _, e := NewFramer(strings.NewReader(""), o.Mode); e != nil {
		return nil, e
	}
	if o.Endpoint == "" {
		o.Endpoint = "https://www.youtube.com/api/lounge"
	}
	u, e := url.Parse(o.Endpoint)
	if e != nil || u.User != nil {
		return nil, errors.New("invalid Lounge endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	if o.Endpoint != "https://www.youtube.com/api/lounge" && (u.Scheme != "http" || ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("Lounge endpoint must be canonical or a loopback fixture")
	}
	if len(o.DeviceID) < 1 || len(o.DeviceID) > 128 || len(o.Name) > 128 {
		return nil, errors.New("invalid Lounge identity")
	}
	client := http.Client{Timeout: 45 * time.Second}
	if o.Client != nil {
		client = *o.Client
		client.Timeout = 45 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	o.Client = &client
	return &Lounge{opts: o, ready: make(chan struct{}), aid: -1, rid: 1000}, nil
}
func (l *Lounge) Ready() <-chan struct{} { return l.ready }
func (l *Lounge) request(ctx context.Context, method, path string, q, urlForm url.Values) (*http.Response, error) {
	endpoint := l.opts.Endpoint + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	var body io.Reader
	if urlForm != nil {
		body = strings.NewReader(urlForm.Encode())
	}
	r, e := http.NewRequestWithContext(ctx, method, endpoint, body)
	if e != nil {
		return nil, errors.New("invalid Lounge request")
	}
	if urlForm != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, e := l.opts.Client.Do(r)
	if e != nil {
		return nil, errors.New("Lounge transport failed")
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, loungeHTTPError{resp.StatusCode, strings.Contains(string(b), "Unknown SID")}
	}
	return resp, nil
}
func (l *Lounge) small(ctx context.Context, method, path string, v url.Values) ([]byte, error) {
	r, e := l.request(ctx, method, path, nil, v)
	if e != nil {
		return nil, e
	}
	defer r.Body.Close()
	b, e := io.ReadAll(io.LimitReader(r.Body, 65537))
	if e != nil || len(b) > 65536 {
		return nil, errors.New("Lounge response limit")
	}
	return b, nil
}
func (l *Lounge) loadToken(ctx context.Context) error {
	if l.opts.ScreenID == "" {
		b, e := l.small(ctx, "GET", "/pairing/generate_screen_id", nil)
		if e != nil {
			return e
		}
		id := strings.TrimSpace(string(b))
		if len(id) < 1 || len(id) > 256 || strings.ContainsAny(id, "\r\n\x00") {
			return errors.New("invalid Lounge screen identity")
		}
		if l.opts.SaveScreen != nil {
			if e = l.opts.SaveScreen(id); e != nil {
				return errors.New("screen identity storage failed")
			}
		}
		l.opts.ScreenID = id
	}
	b, e := l.small(ctx, "POST", "/pairing/get_lounge_token_batch", url.Values{"screen_ids": {l.opts.ScreenID}})
	if e != nil {
		return e
	}
	var result struct {
		Screens []struct {
			ScreenID string `json:"screenId"`
			Token    string `json:"loungeToken"`
		}
	}
	if json.Unmarshal(b, &result) != nil {
		return errors.New("invalid Lounge token response")
	}
	for _, s := range result.Screens {
		if s.ScreenID == l.opts.ScreenID && len(s.Token) > 0 && len(s.Token) <= 4096 {
			l.mu.Lock()
			l.token = s.Token
			l.mu.Unlock()
			return nil
		}
	}
	return errors.New("Lounge screen token unavailable")
}
func (l *Lounge) params(kind string) url.Values {
	l.mu.Lock()
	defer l.mu.Unlock()
	v := url.Values{"device": {"LOUNGE_SCREEN"}, "id": {l.opts.DeviceID}, "name": {l.opts.Name}, "app": {"youtube-desktop"}, "theme": {"cl"}, "VER": {"8"}, "v": {"2"}, "loungeIdToken": {l.token}}
	if kind == "poll" {
		v.Set("RID", "rpc")
		v.Set("CI", "0")
		v.Set("TYPE", "xmlhttp")
	} else {
		v.Set("RID", strconv.FormatInt(l.rid, 10))
		l.rid++
	}
	if kind != "init" {
		v.Set("SID", l.sid)
		v.Set("gsessionid", l.gs)
		v.Set("AID", strconv.FormatInt(l.aid, 10))
	}
	return v
}
func (l *Lounge) Pair(ctx context.Context, code string) error {
	if !pairingPattern.MatchString(code) {
		return errors.New("invalid pairing code")
	}
	_, e := l.small(ctx, "POST", "/pairing/register_pairing_code", url.Values{"access_type": {"permanent"}, "pairing_code": {code}, "screen_id": {l.opts.ScreenID}, "screen_name": {l.opts.Name}, "device_id": {l.opts.DeviceID}, "app": {"youtube-desktop"}})
	return e
}
func (l *Lounge) Send(ctx context.Context, name string, fields map[string]string) error {
	l.send.Lock()
	defer l.send.Unlock()
	if len(name) == 0 || len(name) > 64 || len(fields) > 32 {
		return errors.New("invalid Lounge message")
	}
	l.mu.Lock()
	ready := l.sid != "" && l.gs != ""
	ofs := l.ofs
	l.mu.Unlock()
	if !ready {
		return errors.New("Lounge not bound")
	}
	form := url.Values{"count": {"1"}, "ofs": {strconv.FormatInt(ofs, 10)}, "req0__sc": {name}}
	for k, v := range fields {
		if len(k) > 64 || len(v) > 2048 {
			return errors.New("Lounge field limit")
		}
		form.Set("req0_"+k, v)
	}
	resp, e := l.request(ctx, "POST", "/bc/bind", l.params("send"), form)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if _, e = io.Copy(io.Discard, io.LimitReader(resp.Body, 65537)); e != nil {
		return errors.New("Lounge send failed")
	}
	l.mu.Lock()
	l.ofs++
	l.mu.Unlock()
	return nil
}
func (l *Lounge) batch(body []byte, dispatch func(Message) error) error {
	var rows [][]json.RawMessage
	if json.Unmarshal(body, &rows) != nil || len(rows) > 256 {
		return errors.New("invalid Lounge records")
	}
	for _, row := range rows {
		var id int64
		var parts []json.RawMessage
		var name string
		if len(row) != 2 || json.Unmarshal(row[0], &id) != nil || id < 0 || json.Unmarshal(row[1], &parts) != nil || len(parts) == 0 || json.Unmarshal(parts[0], &name) != nil || len(name) > 64 {
			return errors.New("invalid Lounge record")
		}
		l.mu.Lock()
		if name == "c" || name == "S" {
			var value string
			if len(parts) < 2 || json.Unmarshal(parts[1], &value) != nil || len(value) > 4096 {
				l.mu.Unlock()
				return errors.New("invalid Lounge handshake")
			}
			if name == "c" {
				l.sid = value
			} else {
				l.gs = value
			}
		}
		duplicate := id <= l.aid
		if !duplicate {
			l.aid = id
		}
		l.mu.Unlock()
		if duplicate || name == "c" || name == "S" || name == "noop" {
			continue
		}
		values := map[string]string{}
		if len(parts) > 1 {
			var raw map[string]json.RawMessage
			if json.Unmarshal(parts[1], &raw) != nil || len(raw) > 64 {
				return errors.New("invalid Lounge command fields")
			}
			for k, v := range raw {
				var s string
				if json.Unmarshal(v, &s) != nil {
					s = string(v)
				}
				if len(k) > 128 || len(s) > 8192 {
					return errors.New("Lounge command field limit")
				}
				values[k] = s
			}
		}
		if e := dispatch(Message{id, name, values}); e != nil {
			return e
		}
	}
	return nil
}
func (l *Lounge) bind(ctx context.Context, oldSID string, oldAID int64, dispatch func(Message) error) error {
	l.send.Lock()
	defer l.send.Unlock()
	q := l.params("init")
	if oldSID != "" {
		q.Set("OSID", oldSID)
		q.Set("OAID", strconv.FormatInt(oldAID, 10))
	}
	l.mu.Lock()
	l.sid = ""
	l.gs = ""
	l.aid = -1
	l.ofs = 0
	l.mu.Unlock()
	r, e := l.request(ctx, "POST", "/bc/bind", q, url.Values{"count": {"0"}})
	if e != nil {
		return e
	}
	defer r.Body.Close()
	f, _ := NewFramer(r.Body, l.opts.Mode)
	for range 4 {
		b, e := f.Next()
		if e != nil {
			return errors.New("Lounge handshake incomplete")
		}
		if e = l.batch(b, dispatch); e != nil {
			return e
		}
		l.mu.Lock()
		ready := l.sid != "" && l.gs != ""
		l.mu.Unlock()
		if ready {
			return nil
		}
	}
	return errors.New("Lounge handshake limit")
}
func (l *Lounge) Run(ctx context.Context, code string, dispatch func(Message) error) error {
	l.mu.Lock()
	if l.running {
		l.mu.Unlock()
		return errors.New("Lounge already running")
	}
	l.running = true
	l.mu.Unlock()
	defer func() { l.mu.Lock(); l.running = false; l.token = ""; l.mu.Unlock() }()
	if e := l.loadToken(ctx); e != nil {
		return e
	}
	if e := l.bind(ctx, "", 0, dispatch); e != nil {
		return e
	}
	if e := l.Pair(ctx, code); e != nil {
		return e
	}
	l.readyOnce.Do(func() { close(l.ready) })
	failures := 0
	for ctx.Err() == nil {
		r, e := l.request(ctx, "GET", "/bc/bind", l.params("poll"), nil)
		if e == nil {
			f, _ := NewFramer(r.Body, l.opts.Mode)
			for {
				b, readErr := f.Next()
				if readErr != nil {
					e = readErr
					break
				}
				if e = l.batch(b, dispatch); e != nil {
					break
				}
			}
			r.Body.Close()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var h loungeHTTPError
		if errors.As(e, &h) && h.status == 400 && h.unknown {
			l.mu.Lock()
			sid, aid := l.sid, l.aid
			l.mu.Unlock()
			e = l.bind(ctx, sid, aid, dispatch)
		} else if errors.As(e, &h) && (h.status == 410 || h.status == 401 || h.status == 403) {
			if e = l.loadToken(ctx); e == nil {
				e = l.bind(ctx, "", 0, dispatch)
			}
		}
		if e == nil || e == io.EOF {
			failures = 0
		} else {
			failures++
			if failures > 5 {
				return errors.New("Lounge reconnect limit")
			}
		}
		delay := 100 * time.Millisecond
		if failures > 0 {
			delay = time.Duration(failures) * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}
