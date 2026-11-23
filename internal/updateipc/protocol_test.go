package updateipc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

const protocolTestTag = "build-0123456789abcdef0123456789abcdef01234567"

func frameForTest(body string) []byte {
	frame := make([]byte, 2+len(body))
	binary.BigEndian.PutUint16(frame[:2], uint16(len(body)))
	copy(frame[2:], body)
	return frame
}

func TestEncodeRequestWritesLengthPrefixedJSON(t *testing.T) {
	var out bytes.Buffer
	if err := EncodeRequest(&out, protocolTestTag); err != nil {
		t.Fatal(err)
	}
	want := frameForTest(`{"tag":"build-0123456789abcdef0123456789abcdef01234567"}`)
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("request frame = %q, want %q", out.Bytes(), want)
	}
}

func TestDecodeRequestReadsExactlyOneBoundedFrame(t *testing.T) {
	input := append(frameForTest(`{"tag":"`+protocolTestTag+`"}`), []byte("next-frame")...)
	r := bytes.NewReader(input)
	got, err := DecodeRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != protocolTestTag {
		t.Fatalf("tag = %q", got.Tag)
	}
	if rest, _ := io.ReadAll(r); string(rest) != "next-frame" {
		t.Fatalf("decoder consumed beyond its frame: %q", rest)
	}
}

func TestDecodeRequestRejectsMalformedFrames(t *testing.T) {
	cases := map[string][]byte{
		"empty frame":            frameForTest(""),
		"missing tag":            frameForTest(`{}`),
		"unknown field":          frameForTest(`{"tag":"` + protocolTestTag + `","command":"run"}`),
		"duplicate tag":          frameForTest(`{"tag":"` + protocolTestTag + `","tag":"build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
		"case variant duplicate": frameForTest(`{"tag":"` + protocolTestTag + `","TAG":"build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
		"case variant key":       frameForTest(`{"TAG":"` + protocolTestTag + `"}`),
		"trailing JSON":          frameForTest(`{"tag":"` + protocolTestTag + `"}{}`),
		"invalid JSON":           frameForTest(`{"tag":`),
		"zero length":            {0, 0},
		"too large":              append([]byte{1, 1}, bytes.Repeat([]byte{'x'}, 257)...),
		"short header":           {0},
		"short body":             {0, 4, '{', '}'},
		"bad tag type":           frameForTest(`{"tag":false}`),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeRequest(bytes.NewReader(input)); err == nil {
				t.Fatal("DecodeRequest() succeeded; want protocol error")
			}
		})
	}
}

func TestDecodeRequestBoundsLengthRead(t *testing.T) {
	r := &countingReader{r: bytes.NewReader([]byte{1, 1}), max: 2}
	if _, err := DecodeRequest(r); err == nil {
		t.Fatal("DecodeRequest() succeeded with a declared body missing")
	}
	if r.read > 2 {
		t.Fatalf("read %d bytes, crossed declared frame boundary", r.read)
	}
}

type countingReader struct {
	r    io.Reader
	max  int
	read int
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.read == r.max {
		return 0, errors.New("reader boundary exceeded")
	}
	if len(p) > r.max-r.read {
		p = p[:r.max-r.read]
	}
	n, err := r.r.Read(p)
	r.read += n
	return n, err
}

func TestEncodeAndDecodeResponseRequireConsistentAcknowledgement(t *testing.T) {
	var out bytes.Buffer
	if err := EncodeResponse(&out, Response{Accepted: true, Job: "bookmarksync-update-0123456789abcdef0123456789abcdef01234567"}); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeResponse(bytes.NewReader(out.Bytes()))
	if err != nil || !got.Accepted || got.Job != "bookmarksync-update-0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("decoded response = %#v, error %v", got, err)
	}
	for _, response := range []Response{{Accepted: true}, {Job: "unexpected"}} {
		if err := EncodeResponse(io.Discard, response); err == nil {
			t.Errorf("EncodeResponse(%#v) succeeded; want error", response)
		}
	}
}

func TestDecodeResponseRejectsUnknownOrTrailingFields(t *testing.T) {
	for _, body := range []string{`{"accepted":true,"job":"job","command":"exec"}`, `{"accepted":true,"job":"job"}{}`, `{"accepted":true,"accepted":false,"job":"job"}`} {
		if _, err := DecodeResponse(bytes.NewReader(frameForTest(body))); err == nil {
			t.Errorf("DecodeResponse(%s) succeeded; want error", body)
		}
	}
}

func TestDecodeRequestDoesNotEchoUntrustedContentInErrors(t *testing.T) {
	secret := "not-a-real-command-" + strings.Repeat("x", 270)
	_, err := DecodeRequest(bytes.NewReader(frameForTest(secret)))
	if err == nil {
		t.Fatal("DecodeRequest() succeeded; want error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error echoed untrusted frame: %v", err)
	}
}
