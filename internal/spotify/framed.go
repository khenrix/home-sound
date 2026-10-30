// Package spotify consumes the separately licensed helper's versioned protocol.
package spotify

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/khenrix/home-sound/internal/airplay"
	"github.com/khenrix/home-sound/internal/source"
	"io"
	"math"
	"net"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Input struct {
	Group       string
	Coordinator *source.Coordinator
	TargetDB    float64
	Lease       func(source.Lease)
	Admissions  *Admissions
}

type identity struct {
	Admission  string `json:"admission"`
	Instance   string `json:"instance"`
	URI        string `json:"uri"`
	ArtworkID  string `json:"artwork_id"`
	ArtworkURL string `json:"artwork_url"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Album      string `json:"album"`
}
type record struct {
	Version             int
	Nonce               string
	Generation, Offset  uint64
	Identity            identity
	Reason              string
	Rate, Channels      int
	PCM                 []byte
	Terminal, Transport string
}

func readRecord(c net.Conn) (record, error) {
	var r record
	var h [4]byte
	if _, e := io.ReadFull(c, h[:]); e != nil {
		return r, errors.New("framed helper disconnected")
	}
	n := binary.BigEndian.Uint32(h[:])
	if n == 0 || n > 32768 {
		return r, errors.New("invalid framed record size")
	}
	b := make([]byte, n)
	if _, e := io.ReadFull(c, b); e != nil {
		return r, errors.New("truncated framed record")
	}
	var fields map[string]json.RawMessage
	if !utf8.Valid(b) || json.Unmarshal(b, &fields) != nil {
		return r, errors.New("invalid framed JSON")
	}
	allowed := map[string]bool{"Version": true, "Nonce": true, "Generation": true, "Offset": true, "Identity": true, "Reason": true, "Rate": true, "Channels": true, "PCM": true, "Terminal": true, "Transport": true}
	for k := range fields {
		if !allowed[k] {
			return r, errors.New("unknown framed field")
		}
	}
	if len(fields) != len(allowed) {
		return r, errors.New("missing framed field")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil {
		return r, errors.New("invalid framed field")
	}
	_, e := hex.DecodeString(r.Nonce)
	if r.Version != 2 || len(r.Nonce) != 32 || r.Nonce != strings.ToLower(r.Nonce) || e != nil || r.Rate != 44100 || r.Channels != 2 || len(r.PCM) > 7056 || len(r.PCM)%8 != 0 {
		return r, errors.New("invalid framed format")
	}
	if r.Transport != "playing" && r.Transport != "paused" || r.Terminal != "" && r.Terminal != "track_end" && r.Terminal != "error" {
		return r, errors.New("invalid framed state")
	}
	if r.Generation > 0 && r.Reason != "source" && r.Reason != "seek" && r.Reason != "prefetch" {
		return r, errors.New("invalid framed discontinuity")
	}
	for _, s := range []string{r.Identity.URI, r.Identity.Title, r.Identity.Artist, r.Identity.Album} {
		if len(s) > 1024 || strings.ContainsAny(s, "\r\n\x00") {
			return r, errors.New("invalid framed metadata")
		}
	}
	for n := 0; n < len(r.PCM); n += 4 {
		v := float64(math.Float32frombits(binary.LittleEndian.Uint32(r.PCM[n:])))
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return r, errors.New("nonfinite framed PCM")
		}
	}
	if (airplay.Metadata{Title: r.Identity.Title, Artist: r.Identity.Artist, Album: r.Identity.Album, ArtworkID: r.Identity.ArtworkID, ArtworkURL: r.Identity.ArtworkURL}).Validate() != nil {
		return r, errors.New("invalid artwork metadata")
	}
	return r, nil
}
func (i *Input) Run(ctx context.Context, c net.Conn) error {
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if i.Coordinator == nil || i.Admissions == nil {
		return errors.New("input coordinator required")
	}
	var admission *grant
	var stopGrant func() bool
	defer func() {
		if stopGrant != nil {
			stopGrant()
		}
	}()
	var nonce string
	var gen, next, total uint64
	var meta identity
	var reason string
	var lease source.Lease
	var pipe *packetReader
	var state string
	var trackEnded bool
	defer func() {
		if pipe != nil {
			pipe.Close()
		}
		i.Coordinator.End(lease)
	}()
	for {
		if pipe != nil && (admission != nil && admission.ctx.Err() == nil) && state == "playing" && !trackEnded && !pipe.capacity() {
			if e := pipe.waitSpace(ctx); e != nil {
				return e
			}
		}
		credit := byte(2)
		if pipe == nil || (admission != nil && admission.ctx.Err() == nil) && state == "playing" && pipe.capacity() {
			credit = 1
		}
		c.SetDeadline(time.Now().Add(2 * time.Second))
		if _, e := c.Write([]byte{credit}); e != nil {
			return errors.New("framed helper disconnected")
		}
		r, e := readRecord(c)
		if e != nil {
			return e
		}
		if nonce == "" {
			nonce = r.Nonce
		}
		if nonce != r.Nonce || r.Generation < gen {
			return errors.New("stale framed instance or generation")
		}
		if r.Generation == 0 {
			if len(r.PCM) != 0 || r.Offset != 0 {
				return errors.New("PCM without source")
			}
		} else {
			if admission == nil {
				admission, lease, e = i.Admissions.bind(r.Identity.Admission, r.Identity.Instance, nonce, r.Generation)
				if e != nil {
					return e
				}
				stopGrant = context.AfterFunc(admission.ctx, func() { c.Close() })
			}
			if r.Identity.Admission != admission.token || r.Identity.Instance != i.Admissions.instance || admission.ctx.Err() != nil {
				return errors.New("revoked framed admission")
			}
			if r.Generation != gen {
				if r.Offset != 0 {
					return errors.New("new source offset is not zero")
				}

				first := gen == 0
				natural := gen != 0 && r.Reason == "prefetch" && pipe != nil && (admission != nil && admission.ctx.Err() == nil)
				trackEnded = false
				gen = r.Generation
				next = 0
				meta = r.Identity
				reason = r.Reason
				metadata := airplay.Metadata{Title: meta.Title, Artist: meta.Artist, Album: meta.Album, ArtworkID: meta.ArtworkID, ArtworkURL: meta.ArtworkURL}
				if natural {
					nextLease, advanceErr := i.Admissions.transition(admission, lease, func() (source.Lease, error) { return i.Coordinator.Advance(lease, gen, (total*160+146)/147, metadata) })
					if advanceErr != nil {
						return advanceErr
					}
					lease = nextLease
				} else {
					total = 0
					if pipe != nil {
						pipe.Close()
					}
					pipe = newPacketReader()
					if admission != nil && admission.ctx.Err() == nil {
						pcm, e := source.NewPCM(pipe, 44100, metadata)
						if e != nil {
							return e
						}
						old := lease
						nextLease, transitionErr := i.Admissions.transition(admission, old, func() (source.Lease, error) {
							if first {
								return old, i.Coordinator.Commit(old, pcm, r.Transport == "paused", i.TargetDB, admission.cancel)
							}
							return i.Coordinator.Replace(old, gen, pcm, r.Transport == "paused", i.TargetDB, admission.cancel)
						})
						if transitionErr != nil {
							pcm.Close()
							return transitionErr
						}
						lease = nextLease
					}
				}
				if (admission != nil && admission.ctx.Err() == nil) && i.Lease != nil {
					i.Lease(lease)
				}

			}
			if r.Identity != meta || r.Reason != reason || r.Offset != next {
				return errors.New("noncontiguous framed source")
			}
			if len(r.PCM) > 0 {
				if credit != 1 {
					return errors.New("unsolicited PCM")
				}
				next += uint64(len(r.PCM) / 8)
				total += uint64(len(r.PCM) / 8)
				if admission != nil && admission.ctx.Err() == nil {
					if !pipe.offer(r.PCM) {
						return errors.New("framed PCM overflow")
					}
				}
			}
			if r.Terminal == "error" {
				return errors.New("framed decoder failed")
			}
			if r.Terminal == "track_end" {
				trackEnded = true
				pipe.finish()
			}
			if r.Transport != state && !trackEnded {
				i.Coordinator.Transport(lease, r.Transport)
			}
		}
		state = r.Transport
		if credit == 2 && pipe != nil && (admission != nil && admission.ctx.Err() == nil) && state == "playing" && !trackEnded {
			if !pipe.capacity() {
				if e := pipe.waitSpace(ctx); e != nil {
					return e
				}
			}
			continue
		}
		if credit == 2 || len(r.PCM) == 0 {
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
}

type packetReader struct {
	packets     chan []byte
	space       chan struct{}
	done, end   chan struct{}
	once, ended sync.Once
	pending     []byte
}

func newPacketReader() *packetReader {
	return &packetReader{packets: make(chan []byte, 1), space: make(chan struct{}, 1), done: make(chan struct{}), end: make(chan struct{})}
}
func (p *packetReader) capacity() bool {
	select {
	case <-p.done:
		return false
	case <-p.end:
		return false
	default:
		return len(p.packets) == 0
	}
}
func (p *packetReader) offer(b []byte) bool {
	select {
	case <-p.space:
	default:
	}
	select {
	case <-p.done:
		return true
	default:
	}
	select {
	case p.packets <- b:
		return true
	default:
		return false
	}
}
func (p *packetReader) finish() { p.ended.Do(func() { close(p.end) }) }
func (p *packetReader) Read(ctx context.Context, b []byte) (int, error) {
	if len(p.pending) == 0 {
		select {
		case v := <-p.packets:
			p.pending = v
		default:
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-p.done:
				return 0, io.EOF
			case v := <-p.packets:
				p.pending = v
			case <-p.end:
				select {
				case v := <-p.packets:
					p.pending = v
				default:
					return 0, io.EOF
				}
			}
		}

		select {
		case p.space <- struct{}{}:
		default:
		}
	}
	n := copy(b, p.pending)
	p.pending = p.pending[n:]
	return n, nil
}
func (p *packetReader) Close() error { p.once.Do(func() { close(p.done) }); return nil }

func (p *packetReader) waitSpace(ctx context.Context) error {
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return nil
	case <-p.space:
		return nil
	case <-timer.C:
		return nil
	}
}
