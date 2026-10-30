// Package youtube implements the explicitly enabled DIAL/Lounge input boundary.
package youtube

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"
)

const maxBatch = 65536

type Framer struct {
	r    *bufio.Reader
	mode string
}

func NewFramer(r io.Reader, mode string) (*Framer, error) {
	if mode != "bytes" && mode != "utf16" {
		return nil, errors.New("explicit Lounge framing mode bytes or utf16 required")
	}
	return &Framer{bufio.NewReaderSize(r, 4096), mode}, nil
}
func (p *Framer) Next() ([]byte, error) {
	var digits []byte
	for {
		b, e := p.r.ReadByte()
		if e != nil {
			if e == io.EOF && len(digits) == 0 {
				return nil, io.EOF
			}
			return nil, errors.New("truncated Lounge length")
		}
		if b == '\n' {
			break
		}
		if b < '0' || b > '9' || len(digits) >= 5 {
			return nil, errors.New("invalid Lounge length")
		}
		digits = append(digits, b)
	}
	if len(digits) == 0 || digits[0] == '0' {
		return nil, errors.New("invalid Lounge length")
	}
	n, e := strconv.Atoi(string(digits))
	if e != nil || n > maxBatch {
		return nil, errors.New("Lounge batch too large")
	}
	var body []byte
	if p.mode == "bytes" {
		body = make([]byte, n)
		if _, e = io.ReadFull(p.r, body); e != nil {
			return nil, errors.New("truncated Lounge batch")
		}
	} else {
		for used := 0; used < n; {
			r, size, e := p.r.ReadRune()
			if e != nil || r == utf8.RuneError && size == 1 {
				return nil, errors.New("invalid Lounge UTF-8")
			}
			units := 1
			if r > 0xffff {
				units = 2
			}
			used += units
			if used > n || len(body)+size > maxBatch {
				return nil, errors.New("Lounge unit or byte limit")
			}
			body = utf8.AppendRune(body, r)
		}
	}
	if !utf8.Valid(body) || !json.Valid(body) {
		return nil, errors.New("invalid Lounge JSON batch")
	}
	return body, nil
}
