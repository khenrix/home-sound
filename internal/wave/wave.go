// Package wave reads and writes bounded PCM16 RIFF/WAVE audio.
package wave

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/khenrix/home-sound/internal/audio"
)

const maxBytes = 64 << 20

func Read(r io.Reader) (audio.Format, []float32, error) {
	var zero audio.Format
	b, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return zero, nil, err
	}
	if len(b) > maxBytes {
		return zero, nil, errors.New("WAV too large")
	}
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return zero, nil, errors.New("invalid WAV header")
	}
	size := uint64(binary.LittleEndian.Uint32(b[4:8])) + 8
	if size != uint64(len(b)) {
		return zero, nil, errors.New("truncated or inconsistent RIFF size")
	}
	var format audio.Format
	var data []byte
	foundFmt, foundData := false, false
	for p := 12; p < len(b); {
		if len(b)-p < 8 {
			return zero, nil, errors.New("truncated chunk header")
		}
		id := string(b[p : p+4])
		n := int(binary.LittleEndian.Uint32(b[p+4 : p+8]))
		p += 8
		if n > len(b)-p {
			return zero, nil, errors.New("truncated chunk")
		}
		chunk := b[p : p+n]
		p += n
		if n%2 != 0 {
			if p >= len(b) {
				return zero, nil, errors.New("missing chunk padding")
			}
			p++
		}
		switch id {
		case "fmt ":
			if foundFmt || n < 16 {
				return zero, nil, errors.New("invalid format chunk")
			}
			foundFmt = true
			encoding := binary.LittleEndian.Uint16(chunk)
			channels := int(binary.LittleEndian.Uint16(chunk[2:]))
			rate := int(binary.LittleEndian.Uint32(chunk[4:]))
			byteRate := binary.LittleEndian.Uint32(chunk[8:])
			align := binary.LittleEndian.Uint16(chunk[12:])
			bits := binary.LittleEndian.Uint16(chunk[14:])
			if encoding != 1 || bits != 16 || channels < 1 || rate < 1 || uint64(channels)*2 != uint64(align) || uint64(rate)*uint64(align) != uint64(byteRate) {
				return zero, nil, errors.New("unsupported or invalid PCM format")
			}
			format = audio.Format{Rate: rate, Channels: channels, SampleFormat: audio.Float32}
		case "data":
			if foundData {
				return zero, nil, errors.New("duplicate data chunk")
			}
			foundData = true
			data = chunk
		}
	}
	if !foundFmt || !foundData || len(data)%(format.Channels*2) != 0 {
		return zero, nil, errors.New("missing or unaligned WAV data")
	}
	samples := make([]float32, len(data)/2)
	for i := range samples {
		samples[i] = float32(int16(binary.LittleEndian.Uint16(data[i*2:]))) / 32768
	}
	return format, samples, nil
}
func Write(w io.Writer, format audio.Format, samples []float32) error {
	if err := (audio.Frame{Data: samples}).Validate(format); err != nil {
		return err
	}
	if len(samples) > (maxBytes-44)/2 || uint64(len(samples))*2 > math.MaxUint32-36 {
		return errors.New("WAV too large")
	}
	data := make([]byte, 44+len(samples)*2)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], uint16(format.Channels))
	binary.LittleEndian.PutUint32(data[24:], uint32(format.Rate))
	binary.LittleEndian.PutUint32(data[28:], uint32(format.Rate*format.Channels*2))
	binary.LittleEndian.PutUint16(data[32:], uint16(format.Channels*2))
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(len(samples)*2))
	for i, v := range samples {
		p, err := audio.PCM16(v)
		if err != nil {
			return err
		}
		binary.LittleEndian.PutUint16(data[44+i*2:], uint16(p))
	}
	n, err := w.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return fmt.Errorf("write WAV: %w", io.ErrShortWrite)
	}
	return nil
}
