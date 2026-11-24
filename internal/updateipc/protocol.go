// Package updateipc defines the bounded wire protocol used by the Windows
// application service to request an approved update.
package updateipc

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"regexp"

	"github.com/atikansari-ghr/BookmarksSync/internal/geolite"
)

const maxFrameBytes = 1024

var errProtocol = errors.New("invalid updater pipe message")

type Request struct {
	Tag string `json:"tag"`
}

type Response struct {
	Accepted bool   `json:"accepted"`
	Job      string `json:"job,omitempty"`
}

const (
	GeoLiteOperationStatus    = "status"
	GeoLiteOperationConfigure = "configure"
	GeoLiteOperationClear     = "clear"
	GeoLiteOperationRun       = "run"
)

type GeoLiteRequest struct {
	Operation string          `json:"operation"`
	Config    *geolite.Config `json:"config,omitempty"`
}

type GeoLiteResponse struct {
	OK        bool            `json:"ok"`
	Job       string          `json:"job,omitempty"`
	ErrorCode string          `json:"errorCode,omitempty"`
	Status    *geolite.Status `json:"status,omitempty"`
}

var geoLiteJobPattern = regexp.MustCompile(`^bookmarksync-geolite-[a-z0-9-]{1,80}$`)

var geoLiteErrorCodes = map[string]bool{
	"invalid_request": true,
	"busy":            true,
	"unavailable":     true,
	"update_failed":   true,
}

func EncodeRequest(w io.Writer, tag string) error {
	if tag == "" {
		return errProtocol
	}
	return writeFrame(w, Request{Tag: tag})
}

func DecodeRequest(r io.Reader) (Request, error) {
	var req Request
	if err := readFrame(r, &req, "tag"); err != nil || req.Tag == "" {
		return Request{}, errProtocol
	}
	return req, nil
}

// DecodeServiceRequest distinguishes the legacy release request from the
// bounded GeoLite operation envelope without weakening either decoder.
func DecodeServiceRequest(r io.Reader) (Request, *GeoLiteRequest, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Request{}, nil, errProtocol
	}
	n := int(binary.BigEndian.Uint16(header[:]))
	if n == 0 || n > maxFrameBytes {
		return Request{}, nil, errProtocol
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return Request{}, nil, errProtocol
	}
	var discriminator map[string]json.RawMessage
	if json.Unmarshal(body, &discriminator) != nil {
		return Request{}, nil, errProtocol
	}
	framed := bytes.NewReader(append(header[:], body...))
	if _, ok := discriminator["operation"]; ok {
		request, err := DecodeGeoLiteRequest(framed)
		if err != nil {
			return Request{}, nil, err
		}
		return Request{}, &request, nil
	}
	request, err := DecodeRequest(framed)
	if err != nil {
		return Request{}, nil, err
	}
	return request, nil, nil
}

func EncodeResponse(w io.Writer, response Response) error {
	if response.Accepted != (response.Job != "") {
		return errProtocol
	}
	return writeFrame(w, response)
}

func DecodeResponse(r io.Reader) (Response, error) {
	var response Response
	if err := readFrame(r, &response, "accepted", "job"); err != nil || response.Accepted != (response.Job != "") {
		return Response{}, errProtocol
	}
	return response, nil
}

func EncodeGeoLiteRequest(w io.Writer, request GeoLiteRequest) error {
	if err := validateGeoLiteRequest(request); err != nil {
		return err
	}
	return writeFrame(w, request)
}

func DecodeGeoLiteRequest(r io.Reader) (GeoLiteRequest, error) {
	var request GeoLiteRequest
	keys := objectKeyRules{
		"$":        keySet("operation", "config"),
		"$.config": keySet("accountId", "licenseKey", "enabled", "days", "startTime"),
	}
	if err := readFrameWithKeys(r, &request, keys); err != nil || validateGeoLiteRequest(request) != nil {
		return GeoLiteRequest{}, errProtocol
	}
	return request, nil
}

func validateGeoLiteRequest(request GeoLiteRequest) error {
	switch request.Operation {
	case GeoLiteOperationStatus, GeoLiteOperationClear, GeoLiteOperationRun:
		if request.Config != nil {
			return errProtocol
		}
	case GeoLiteOperationConfigure:
		if request.Config == nil || geolite.ValidateConfig(*request.Config) != nil {
			return errProtocol
		}
	default:
		return errProtocol
	}
	return nil
}

func EncodeGeoLiteResponse(w io.Writer, response GeoLiteResponse) error {
	if err := validateGeoLiteResponse(response); err != nil {
		return err
	}
	return writeFrame(w, response)
}

func DecodeGeoLiteResponse(r io.Reader) (GeoLiteResponse, error) {
	var response GeoLiteResponse
	keys := objectKeyRules{
		"$":        keySet("ok", "job", "errorCode", "status"),
		"$.status": keySet("configured", "enabled", "days", "startTime", "databaseAvailable", "databaseBuildTime", "lastAttempt", "lastResult", "lastErrorCode", "running", "recoveryRequired"),
	}
	if err := readFrameWithKeys(r, &response, keys); err != nil || validateGeoLiteResponse(response) != nil {
		return GeoLiteResponse{}, errProtocol
	}
	return response, nil
}

func validateGeoLiteResponse(response GeoLiteResponse) error {
	if response.OK {
		if response.ErrorCode != "" || (response.Job != "" && !geoLiteJobPattern.MatchString(response.Job)) {
			return errProtocol
		}
		return nil
	}
	if response.Job != "" || !geoLiteErrorCodes[response.ErrorCode] {
		return errProtocol
	}
	return nil
}

func writeFrame(w io.Writer, value any) error {
	body, err := json.Marshal(value)
	if err != nil || len(body) == 0 || len(body) > maxFrameBytes {
		return errProtocol
	}
	var header [2]byte
	binary.BigEndian.PutUint16(header[:], uint16(len(body)))
	if err := writeAll(w, header[:]); err != nil {
		return errProtocol
	}
	if err := writeAll(w, body); err != nil {
		return errProtocol
	}
	return nil
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if n > 0 {
			b = b[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func readFrame(r io.Reader, out any, allowed ...string) error {
	return readFrameWithKeys(r, out, objectKeyRules{"$": keySet(allowed...)})
}

type objectKeyRules map[string]map[string]bool

func keySet(keys ...string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return set
}

func readFrameWithKeys(r io.Reader, out any, rules objectKeyRules) error {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return errProtocol
	}
	n := int(binary.BigEndian.Uint16(header[:]))
	if n == 0 || n > maxFrameBytes {
		return errProtocol
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return errProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := rejectJSONKeys(body, rules); err != nil {
		return errProtocol
	}
	if err := decoder.Decode(out); err != nil {
		return errProtocol
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errProtocol
	}
	return nil
}

// rejectJSONKeys requires exact, unique keys for each security-sensitive JSON
// object. This avoids case-insensitive field matching and last-value-wins
// behavior during strict pipe decoding.
func rejectJSONKeys(body []byte, rules objectKeyRules) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := scanJSONValue(decoder, "$", rules); err != nil {
		return errProtocol
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errProtocol
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, path string, rules objectKeyRules) error {
	token, err := decoder.Token()
	if err != nil {
		return errProtocol
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		allowed := rules[path]
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok || seen[key] || (allowed != nil && !allowed[key]) {
				return errProtocol
			}
			seen[key] = true
			if err := scanJSONValue(decoder, path+"."+key, rules); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errProtocol
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, path+"[]", rules); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errProtocol
		}
	default:
		return errProtocol
	}
	return nil
}
