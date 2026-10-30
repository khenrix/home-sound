package youtube

import (
	"io"
	"strings"
	"testing"
)

type splitReader struct{ io.Reader }

func (r splitReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}
func TestLoungeExplicitUnicodeUnits(t *testing.T) {
	for _, tc := range []struct{ mode, input, want string }{{"bytes", "4\n\"å\"", `"å"`}, {"utf16", "3\n\"å\"", `"å"`}, {"bytes", "6\n\"😀\"", `"😀"`}, {"utf16", "4\n\"😀\"", `"😀"`}, {"bytes", "8\n\"\\u00e5\"", `"\u00e5"`}, {"utf16", "8\n\"\\u00e5\"", `"\u00e5"`}, {"bytes", "3\n123", `123`}} {
		t.Run(tc.mode+tc.want, func(t *testing.T) {
			p, e := NewFramer(splitReader{strings.NewReader(tc.input)}, tc.mode)
			if e != nil {
				t.Fatal(e)
			}
			got, e := p.Next()
			if e != nil || string(got) != tc.want {
				t.Fatalf("batch %q %v", got, e)
			}
		})
	}
}
func TestLoungeConsecutiveAndMalformed(t *testing.T) {
	p, e := NewFramer(strings.NewReader("3\n1234\n\"å\""), "bytes")
	if e != nil {
		t.Fatal(e)
	}
	a, e := p.Next()
	if e != nil || string(a) != "123" {
		t.Fatalf("first %s %v", a, e)
	}
	b, e := p.Next()
	if e != nil || string(b) != `"å"` {
		t.Fatalf("second %s %v", b, e)
	}
	if _, e = p.Next(); e != io.EOF {
		t.Fatal(e)
	}
	for _, s := range []string{"-1\n[]", "0002\n[]", "65537\n[]", "999999999999999999999\n[]", "4\n\"å", "2\nx[", "3\n\"😀\""} {
		p, _ := NewFramer(strings.NewReader(s), "utf16")
		if _, e = p.Next(); e == nil {
			t.Fatalf("accepted malformed batch %q", s)
		}
	}
	if _, e = NewFramer(strings.NewReader(""), ""); e == nil {
		t.Fatal("guessed framing mode")
	}
}
