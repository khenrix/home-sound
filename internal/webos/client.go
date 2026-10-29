// Package webos implements local LG registration and native audio control.
package webos

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/khenrix/home-sound/internal/pairing"
)

type Config struct{ URL, PairingFile, CertificateSHA256 string }
type credentials struct {
	Endpoint  string `json:"endpoint"`
	ClientKey string `json:"client_key"`
}
type Volume struct {
	Level        int
	Muted, Valid bool
}
type message struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	URI     string          `json:"uri,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}
type Client struct {
	conn    *websocket.Conn
	mu      sync.Mutex
	next    uint64
	pending map[string]chan message
	done    chan struct{}
}

var errProtocol = errors.New("webOS control failed")

func Connect(ctx context.Context, cfg Config, allowPair bool) (*Client, error) {
	u, e := url.Parse(cfg.URL)
	if e != nil || u.Host == "" || (u.Scheme != "ws" && u.Scheme != "wss") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid webOS endpoint")
	}
	stored := credentials{Endpoint: cfg.URL}
	if !allowPair {
		if pairing.Load(cfg.PairingFile, &stored) != nil || stored.Endpoint != cfg.URL || stored.ClientKey == "" {
			return nil, errors.New("webOS pairing required")
		}
	}
	var opts *websocket.DialOptions
	if cfg.CertificateSHA256 != "" {
		digest, err := hex.DecodeString(cfg.CertificateSHA256)
		if err != nil || len(digest) != 32 || u.Scheme != "wss" {
			return nil, errors.New("valid SHA256 certificate pin requires wss")
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: func(state tls.ConnectionState) error {
			// A deliberate certificate pin replaces CA/hostname verification for local TVs.
			if len(state.PeerCertificates) == 0 {
				return errProtocol
			}
			sum := sha256.Sum256(state.PeerCertificates[0].Raw)
			if !strings.EqualFold(hex.EncodeToString(sum[:]), cfg.CertificateSHA256) {
				return errProtocol
			}
			return nil
		}}
		defer transport.CloseIdleConnections()
		opts = &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}}
	}
	conn, _, e := websocket.Dial(ctx, cfg.URL, opts)
	if e != nil {
		return nil, errProtocol
	}
	conn.SetReadLimit(65536)
	c := &Client{conn: conn, pending: make(map[string]chan message), done: make(chan struct{})}
	go c.read()
	payload := map[string]any{"forcePairing": false, "pairingType": "PROMPT", "manifest": map[string]any{"manifestVersion": 1, "permissions": []string{"CONTROL_AUDIO"}}}
	if stored.ClientKey != "" {
		payload["client-key"] = stored.ClientKey
	}
	id, ch, e := c.send(ctx, "register", "", payload)
	if e != nil {
		c.Close()
		return nil, e
	}
	defer c.forget(id)
	for {
		select {
		case <-ctx.Done():
			c.Close()
			return nil, ctx.Err()
		case <-c.done:
			return nil, errProtocol
		case m := <-ch:
			var p struct {
				Key         string `json:"client-key"`
				PairingType string `json:"pairingType"`
			}
			if json.Unmarshal(m.Payload, &p) != nil {
				c.Close()
				return nil, errProtocol
			}
			if m.Type == "registered" && p.Key != "" {
				if allowPair {
					if e = pairing.Save(cfg.PairingFile, credentials{cfg.URL, p.Key}); e != nil {
						c.Close()
						return nil, e
					}
				}
				return c, nil
			}
			if !allowPair || m.Type != "response" || p.PairingType != "PROMPT" {
				c.Close()
				return nil, errors.New("webOS registration rejected")
			}
		}
	}
}
func (c *Client) Close() error { return c.conn.CloseNow() }
func (c *Client) read() {
	defer c.conn.CloseNow()
	defer close(c.done)
	for {
		var m message
		if wsjson.Read(context.Background(), c.conn, &m) != nil {
			return
		}
		c.mu.Lock()
		ch := c.pending[m.ID]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- m:
			default:
				c.conn.CloseNow()
				return
			}
		}
	}
}
func (c *Client) forget(id string) { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }
func (c *Client) send(ctx context.Context, typ, uri string, payload any) (string, chan message, error) {
	raw, e := json.Marshal(payload)
	if e != nil {
		return "", nil, errProtocol
	}
	c.mu.Lock()
	c.next++
	id := strconv.FormatUint(c.next, 10)
	ch := make(chan message, 16)
	c.pending[id] = ch
	c.mu.Unlock()
	if wsjson.Write(ctx, c.conn, message{id, typ, uri, raw}) != nil {
		c.forget(id)
		return "", nil, errProtocol
	}
	return id, ch, nil
}
func (c *Client) request(ctx context.Context, uri string, p any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	id, ch, e := c.send(ctx, "request", uri, p)
	if e != nil {
		return nil, e
	}
	defer c.forget(id)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, errProtocol
	case m := <-ch:
		var p struct {
			OK *bool `json:"returnValue"`
		}
		if m.Type != "response" || json.Unmarshal(m.Payload, &p) != nil || p.OK == nil || !*p.OK {
			return nil, errProtocol
		}
		return m.Payload, nil
	}
}
func (c *Client) SetVolume(ctx context.Context, level int) error {
	// All exposed controls are calibration-safe; playback uses exactly 10.
	if level < 0 || level > 15 {
		return errors.New("native calibration volume must be 0 through 15")
	}
	_, e := c.request(ctx, "ssap://audio/setVolume", map[string]int{"volume": level})
	return e
}
func (c *Client) Volume(ctx context.Context) (Volume, error) {
	raw, e := c.request(ctx, "ssap://audio/getVolume", nil)
	if e != nil {
		return Volume{}, e
	}
	return decodeVolume(raw)
}
func decodeVolume(raw json.RawMessage) (Volume, error) {
	var p struct {
		OK     *bool `json:"returnValue"`
		Level  *int  `json:"volume"`
		Muted  *bool `json:"muted"`
		Status *struct {
			Level *int  `json:"volume"`
			Muted *bool `json:"muteStatus"`
		} `json:"volumeStatus"`
	}
	if json.Unmarshal(raw, &p) != nil || (p.OK != nil && !*p.OK) {
		return Volume{}, errProtocol
	}
	if p.Status != nil {
		p.Level = p.Status.Level
		p.Muted = p.Status.Muted
	}
	if p.Level == nil || p.Muted == nil || *p.Level < 0 || *p.Level > 100 {
		return Volume{}, errProtocol
	}
	return Volume{*p.Level, *p.Muted, true}, nil
}
func (c *Client) WatchVolume(ctx context.Context) (<-chan Volume, error) {
	id, ch, e := c.send(ctx, "subscribe", "ssap://audio/getVolume", nil)
	if e != nil {
		return nil, e
	}
	out := make(chan Volume, 16)
	go func() {
		defer close(out)
		defer c.forget(id)
		for {
			select {
			case <-ctx.Done():
				return
			case <-c.done:
				return
			case m := <-ch:
				v, e := decodeVolume(m.Payload)
				if m.Type != "response" || e != nil {
					return
				}
				select {
				case out <- v:
				case <-ctx.Done():
					return
				default:
					c.Close()
					return
				}
			}
		}
	}()
	return out, nil
}
