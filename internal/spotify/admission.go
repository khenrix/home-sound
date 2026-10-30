package spotify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/khenrix/home-sound/internal/source"
	"io"
	"net"
	"sync"
	"time"
)

type AdmissionRequest struct {
	Instance string
	Sequence uint64
	Received int64
	Retain   string
}
type grant struct {
	token  string
	lease  source.Lease
	ctx    context.Context
	cancel context.CancelFunc
	nonce  string
}
type Admissions struct {
	mu              sync.Mutex
	ctx             context.Context
	co              *source.Coordinator
	group, instance string
	sequence        uint64
	current         *grant
}

func NewAdmissions(ctx context.Context, co *source.Coordinator, group, instance string) *Admissions {
	return &Admissions{ctx: ctx, co: co, group: group, instance: instance}
}
func (b *Admissions) Request(r AdmissionRequest) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ctx.Err() != nil || r.Instance != b.instance || r.Sequence <= b.sequence || r.Received <= 0 {
		return "", errors.New("invalid native command")
	}
	b.sequence = r.Sequence
	if g := b.current; r.Retain != "" && g != nil && r.Retain == g.token && g.ctx.Err() == nil && b.co.Current(g.lease) {
		return g.token, nil
	}
	run, cancel := context.WithCancel(b.ctx)
	l, e := b.co.ReserveNative(source.Identity{Provider: "spotify", Group: b.group, Instance: b.instance, Generation: r.Sequence}, r.Received, cancel)
	if e != nil {
		cancel()
		return "", e
	}
	var token [16]byte
	if _, e = rand.Read(token[:]); e != nil {
		cancel()
		b.co.End(l)
		return "", errors.New("native grant unavailable")
	}
	g := &grant{token: hex.EncodeToString(token[:]), lease: l, ctx: run, cancel: cancel}
	b.current = g
	return g.token, nil
}
func (b *Admissions) bind(token, instance, nonce string, generation uint64) (*grant, source.Lease, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	g := b.current
	if g == nil || g.token != token || instance != b.instance || g.ctx.Err() != nil || !b.co.Current(g.lease) {
		return nil, source.Lease{}, errors.New("revoked framed admission")
	}
	if g.nonce != "" {
		return nil, source.Lease{}, errors.New("admission already bound to an output")
	}
	l, e := b.co.BindNative(g.lease, source.Identity{Provider: "spotify", Group: b.group, Instance: nonce, Generation: generation})
	if e != nil {
		return nil, source.Lease{}, e
	}
	g.nonce = nonce
	g.lease = l
	return g, l, nil
}

// transition serializes generation/epoch changes with native retain requests.
// The cancellation callback only cancels transport and never re-enters this lock.
func (b *Admissions) transition(g *grant, before source.Lease, apply func() (source.Lease, error)) (source.Lease, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.current != g || g.ctx.Err() != nil || g.lease != before {
		return source.Lease{}, errors.New("revoked source transition")
	}
	l, e := apply()
	if e == nil {
		g.lease = l
	}
	return l, e
}

// Serve accepts only bounded private Unix IPC. Admission callbacks cancel the
// previous grant; they never synchronously call a helper's blocked REST loop.
func (b *Admissions) Serve(ctx context.Context, l net.Listener) error {
	stop := context.AfterFunc(ctx, func() { l.Close() })
	defer stop()
	for {
		c, e := l.Accept()
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("admission listener unavailable")
		}
		c.SetDeadline(time.Now().Add(500 * time.Millisecond))
		var r AdmissionRequest
		d := json.NewDecoder(io.LimitReader(c, 1025))
		d.DisallowUnknownFields()
		e = d.Decode(&r)
		var token string
		if e == nil {
			token, e = b.Request(r)
		}
		reply := struct {
			Grant    string
			Retained bool
		}{token, token != "" && token == r.Retain}
		if e != nil {
			reply.Grant = ""
		}
		writeErr := json.NewEncoder(c).Encode(reply)
		c.Close()
		if writeErr != nil && token != "" {
			b.mu.Lock()
			g := b.current
			if g != nil && g.token == token {
				g.cancel()
				b.co.End(g.lease)
			}
			b.mu.Unlock()
		}
	}
}
