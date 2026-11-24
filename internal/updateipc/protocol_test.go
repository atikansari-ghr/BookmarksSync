package updateipc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/geolite"
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
		"too large":              append([]byte{4, 1}, bytes.Repeat([]byte{'x'}, 1025)...),
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

func TestGeoLiteRequestRoundTripsClosedOperations(t *testing.T) {
	config := &geolite.Config{AccountID: "12345", LicenseKey: "secret-key", Enabled: true, Days: []string{"MON", "THU"}, StartTime: "04:00"}
	for _, request := range []GeoLiteRequest{
		{Operation: GeoLiteOperationStatus},
		{Operation: GeoLiteOperationConfigure, Config: config},
		{Operation: GeoLiteOperationClear},
		{Operation: GeoLiteOperationRun},
	} {
		t.Run(request.Operation, func(t *testing.T) {
			var frame bytes.Buffer
			if err := EncodeGeoLiteRequest(&frame, request); err != nil {
				t.Fatal(err)
			}
			got, err := DecodeGeoLiteRequest(bytes.NewReader(frame.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			if got.Operation != request.Operation || (got.Config == nil) != (request.Config == nil) {
				t.Fatalf("decoded request = %#v; want %#v", got, request)
			}
			if got.Config != nil {
				if got.Config.AccountID != request.Config.AccountID || got.Config.LicenseKey != request.Config.LicenseKey || got.Config.Enabled != request.Config.Enabled || got.Config.StartTime != request.Config.StartTime || strings.Join(got.Config.Days, ",") != strings.Join(request.Config.Days, ",") {
					t.Fatalf("decoded config = %#v; want %#v", *got.Config, *request.Config)
				}
			}
		})
	}
}

func TestDecodeGeoLiteRequestRejectsUnknownMalformedAndAmbiguousMessages(t *testing.T) {
	for _, body := range []string{
		`{"operation":"exec"}`,
		`{"operation":"configure"}`,
		`{"operation":"configure","config":{"accountId":"123","licenseKey":"key","enabled":true,"days":["MON"],"startTime":"04:00"}}`,
		`{"operation":"configure","config":{"accountId":"123","licenseKey":"key","enabled":true,"days":["MON","MON"],"startTime":"04:00"}}`,
		`{"operation":"status","config":{"enabled":false,"days":["MON","THU"],"startTime":"04:00"}}`,
		`{"operation":"status","path":"C:\\secrets"}`,
		`{"operation":"status","operation":"clear"}`,
		`{"operation":"configure","config":{"accountId":"123","accountId":"456","licenseKey":"key","enabled":true,"days":["MON","THU"],"startTime":"04:00"}}`,
		`{"operation":"configure","config":{"accountId":"123","AccountID":"456","licenseKey":"key","enabled":true,"days":["MON","THU"],"startTime":"04:00"}}`,
		`{"operation":"configure","config":{"accountId":"123","licenseKey":"key\nEditionIDs GeoIP2-City","enabled":true,"days":["MON","THU"],"startTime":"04:00"}}`,
	} {
		if _, err := DecodeGeoLiteRequest(bytes.NewReader(frameForTest(body))); err == nil {
			t.Errorf("DecodeGeoLiteRequest(%s) succeeded; want protocol error", body)
		}
	}
}

func TestGeoLiteResponseRoundTripsAndRejectsUnapprovedErrorCodes(t *testing.T) {
	response := GeoLiteResponse{
		OK:     true,
		Status: &geolite.Status{Configured: true, Enabled: true, Days: []string{"MON", "THU"}, StartTime: "04:00", DatabaseAvailable: true},
	}
	var frame bytes.Buffer
	if err := EncodeGeoLiteResponse(&frame, response); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeGeoLiteResponse(bytes.NewReader(frame.Bytes()))
	if err != nil || !got.OK || got.Status == nil || !got.Status.DatabaseAvailable {
		t.Fatalf("decoded response = %#v, error %v", got, err)
	}
	for _, invalid := range []GeoLiteResponse{
		{OK: true, ErrorCode: "failed"},
		{ErrorCode: "C:\\private\\GeoIP.conf"},
		{ErrorCode: "busy", Job: "untrusted-job"},
	} {
		if err := EncodeGeoLiteResponse(io.Discard, invalid); err == nil {
			t.Errorf("EncodeGeoLiteResponse(%#v) succeeded; want error", invalid)
		}
	}
	for _, body := range []string{
		`{"ok":true,"errorCode":"unavailable"}`,
		`{"ok":false,"errorCode":"C:\\private\\GeoIP.conf"}`,
		`{"ok":false,"errorCode":"busy","job":"unexpected"}`,
		`{"ok":true,"status":{"configured":false,"enabled":false,"days":["MON","THU"],"startTime":"04:00","databaseAvailable":false,"lastResult":"none","running":false,"unknown":"value"}}`,
		`{"ok":true,"status":{"configured":false,"Configured":true}}`,
	} {
		if _, err := DecodeGeoLiteResponse(bytes.NewReader(frameForTest(body))); err == nil {
			t.Errorf("DecodeGeoLiteResponse(%s) succeeded; want protocol error", body)
		}
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
