package wave

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/khenrix/home-sound/internal/audio"
)

func TestRoundTrip(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, audio.Canonical, []float32{-1, 1, 0.5, -0.5}); err != nil {
		t.Fatal(err)
	}
	f, s, err := Read(bytes.NewReader(b.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if f != audio.Canonical || len(s) != 4 || s[0] != -1 || s[1] != float32(32767.0/32768) || s[2] != 0.5 || s[3] != -0.5 {
		t.Fatalf("got %#v %#v", f, s)
	}
}

func TestReadRejectsMalformedAndUnsupportedWAV(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, audio.Canonical, []float32{0, 0}); err != nil {
		t.Fatal(err)
	}
	original := b.Bytes()
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"truncated header", original[:20]}, {"truncated samples", original[:len(original)-1]},
		{"bad magic", append([]byte("NOPE"), original[4:]...)},
		{"unsupported encoding", func() []byte { v := bytes.Clone(original); binary.LittleEndian.PutUint16(v[20:], 3); return v }()},
		{"unsupported bits", func() []byte { v := bytes.Clone(original); binary.LittleEndian.PutUint16(v[34:], 24); return v }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := Read(bytes.NewReader(tc.data)); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
}
