package source

import (
	"bytes"
	"context"
	"encoding/binary"
	"github.com/khenrix/home-sound/internal/airplay"
	"io"
	"math"
	"testing"
)

type byteFixture struct {
	*bytes.Reader
	limit, closed int
}

func (r *byteFixture) Read(ctx context.Context, p []byte) (int, error) {
	if e := ctx.Err(); e != nil {
		return 0, e
	}
	if r.limit > 0 && len(p) > r.limit {
		p = p[:r.limit]
	}
	return r.Reader.Read(p)
}
func (r *byteFixture) Close() error { r.closed++; return nil }
func TestPCMResamplesContinuousSplitReadsAndEOF(t *testing.T) {
	b := make([]byte, 441*8)
	for i := 0; i < len(b); i += 4 {
		binary.LittleEndian.PutUint32(b[i:], math.Float32bits(.25))
	}
	r := &byteFixture{Reader: bytes.NewReader(b), limit: 7}
	p, e := NewPCM(r, 44100, airplay.Metadata{Title: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	var frames uint64
	for {
		f, e := p.Read(context.Background())
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if f.Sample != frames || len(f.Data) > 1920 || len(f.Data) == 0 {
			t.Fatal("discontinuous PCM")
		}
		frames += uint64(len(f.Data) / 2)
	}
	if frames != 480 {
		t.Fatalf("rational frame count %d", frames)
	}
	p.Close()
	p.Close()
	if r.closed != 1 {
		t.Fatal("double source close")
	}
}
func TestPCMPartialAndNonfiniteFail(t *testing.T) {
	for _, b := range [][]byte{{1, 2, 3}, {0, 0, 128, 127, 0, 0, 0, 0}} {
		p, e := NewPCM(&byteFixture{Reader: bytes.NewReader(b)}, 48000, airplay.Metadata{})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = p.Read(context.Background()); e == nil || e == io.EOF {
			t.Fatalf("accepted malformed PCM %v", e)
		}
		p.Close()
	}
}
