// Package updateipc defines the bounded wire protocol used by the Windows
// application service to request an approved update.
package updateipc

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

const maxFrameBytes = 256

var errProtocol = errors.New("invalid updater pipe message")

type Request struct {
	Tag string `json:"tag"`
}

type Response struct {
	Accepted bool   `json:"accepted"`
	Job      string `json:"job,omitempty"`
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
	if err := rejectDuplicateKeys(body, allowed); err != nil {
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

// rejectDuplicateKeys prevents JSON's last-value-wins behavior from giving
// the sender and receiver different interpretations of a security field.
func rejectDuplicateKeys(body []byte, allowed []string) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errProtocol
	}
	seen := make(map[string]bool)
	allow := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		allow[key] = true
	}
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || !allow[key] || seen[key] {
			return errProtocol
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return errProtocol
		}
	}
	if _, err := decoder.Token(); err != nil {
		return errProtocol
	}
	if decoder.More() {
		return errProtocol
	}
	return nil
}
