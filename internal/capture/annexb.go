package capture

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"
)

var ErrClosed = errors.New("h264 stream closed")

// AnnexBReader extracts H.264 Access Units from an Annex-B byte stream.
//
// The FFmpeg command inserts AUD NALUs. AUD is intentionally used as the frame
// boundary because grouping individual NALUs by slice headers is slower and
// easier to get wrong. WebRTC wants frame samples, not arbitrary single NALUs.
type AnnexBReader struct {
	reader        *bufio.Reader
	frameDuration time.Duration
	pending       []byte
	buffer        []byte
	eof           bool
}

func NewAnnexBReader(r io.Reader, fps int) *AnnexBReader {
	if fps <= 0 {
		fps = 60
	}
	return &AnnexBReader{
		reader:        bufio.NewReaderSize(r, 2<<20),
		frameDuration: time.Second / time.Duration(fps),
	}
}

func (r *AnnexBReader) ReadFrame() (EncodedFrame, error) {
	var au []byte
	if len(r.pending) > 0 {
		au = append(au, r.pending...)
		r.pending = nil
	}

	for {
		nalu, err := r.readNALU()
		if err != nil {
			if errors.Is(err, io.EOF) && len(au) > 0 {
				return buildFrame(au, r.frameDuration)
			}
			if errors.Is(err, io.EOF) {
				return EncodedFrame{}, ErrClosed
			}
			return EncodedFrame{}, err
		}

		naluType := parseNALUType(nalu)
		if naluType == NALUTypeAUD && len(au) > 0 {
			r.pending = append(r.pending[:0], nalu...)
			return buildFrame(au, r.frameDuration)
		}
		au = append(au, nalu...)
	}
}

func (r *AnnexBReader) readNALU() ([]byte, error) {
	for {
		start, startLen := findStartCode(r.buffer, 0)
		if start < 0 {
			if r.eof {
				return nil, io.EOF
			}
			if len(r.buffer) > 3 {
				r.buffer = append([]byte(nil), r.buffer[len(r.buffer)-3:]...)
			}
			if err := r.readMore(); err != nil {
				if errors.Is(err, io.EOF) {
					r.eof = true
					continue
				}
				return nil, err
			}
			continue
		}
		if start > 0 {
			r.buffer = r.buffer[start:]
		}

		next, _ := findStartCode(r.buffer, startLen)
		if next >= 0 {
			nalu := append([]byte(nil), r.buffer[:next]...)
			r.buffer = r.buffer[next:]
			return nalu, nil
		}

		if r.eof {
			if len(r.buffer) == 0 {
				return nil, io.EOF
			}
			nalu := append([]byte(nil), r.buffer...)
			r.buffer = nil
			return nalu, nil
		}

		if err := r.readMore(); err != nil {
			if errors.Is(err, io.EOF) {
				r.eof = true
				continue
			}
			return nil, err
		}
	}
}

func (r *AnnexBReader) readMore() error {
	chunk := make([]byte, 64*1024)
	n, err := r.reader.Read(chunk)
	if n > 0 {
		r.buffer = append(r.buffer, chunk[:n]...)
	}
	return err
}

func buildFrame(data []byte, duration time.Duration) (EncodedFrame, error) {
	if len(data) == 0 {
		return EncodedFrame{}, fmt.Errorf("empty h264 access unit")
	}
	naluTypes := collectNALUTypes(data)
	return EncodedFrame{
		Data:       append([]byte(nil), data...),
		Duration:   duration,
		IsKeyframe: hasNALUType(naluTypes, NALUTypeIDR),
		NALUTypes:  naluTypes,
	}, nil
}

func startCodeLen(buf []byte) int {
	if len(buf) >= 4 && bytes.Equal(buf[len(buf)-4:], []byte{0, 0, 0, 1}) {
		return 4
	}
	if len(buf) >= 3 && bytes.Equal(buf[len(buf)-3:], []byte{0, 0, 1}) {
		return 3
	}
	return 0
}

func findStartCode(data []byte, from int) (int, int) {
	for i := from; i < len(data)-3; i++ {
		if data[i] == 0 && data[i+1] == 0 {
			if data[i+2] == 1 {
				return i, 3
			}
			if data[i+2] == 0 && data[i+3] == 1 {
				return i, 4
			}
		}
	}
	if len(data) >= 3 {
		i := len(data) - 3
		if i >= from && data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			return i, 3
		}
	}
	return -1, 0
}

func parseNALUType(nalu []byte) byte {
	offset := 0
	if len(nalu) >= 4 && bytes.Equal(nalu[:4], []byte{0, 0, 0, 1}) {
		offset = 4
	} else if len(nalu) >= 3 && bytes.Equal(nalu[:3], []byte{0, 0, 1}) {
		offset = 3
	}
	if len(nalu) <= offset {
		return 0
	}
	return nalu[offset] & 0x1f
}

func collectNALUTypes(data []byte) []byte {
	var types []byte
	offset := 0
	for {
		i, n := findStartCode(data, offset)
		if i < 0 {
			return types
		}
		header := i + n
		if header < len(data) {
			types = append(types, data[header]&0x1f)
		}
		offset = header + 1
	}
}

func hasNALUType(types []byte, want byte) bool {
	for _, got := range types {
		if got == want {
			return true
		}
	}
	return false
}
