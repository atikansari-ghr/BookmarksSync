// Package webauthntest is a test-only software WebAuthn authenticator. It answers
// the creation/request options produced by the server with ES256 ("none" attestation)
// responses in the PublicKeyCredential JSON form a browser sends, and exposes knobs to
// forge mismatched origins, RP IDs, flags and counters. It must never be imported by
// production code.
package webauthntest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
)

// Credential is one resident key held by the authenticator.
type Credential struct {
	ID         []byte
	UserHandle []byte
	RPID       string
	Key        *ecdsa.PrivateKey
	SignCount  uint32
}

// Authenticator is a software passkey provider bound to an origin.
type Authenticator struct {
	Origin string
	// UV and BE/BS control the authenticator data flags (UP is always set).
	UV, BE, BS bool
	Creds      []*Credential
}

// New returns an authenticator that reports origin as the calling page.
func New(origin string) *Authenticator {
	return &Authenticator{Origin: origin, UV: true}
}

// Overrides lets a test forge one response.
type Overrides struct {
	Origin     string  // clientData origin (default a.Origin)
	RPID       string  // RP ID hashed into authenticator data (default from options)
	Type       string  // clientData type (default webauthn.create/get)
	Challenge  string  // clientData challenge (default from options)
	SignCount  *uint32 // counter to report (default stored+1)
	UserHandle []byte  // userHandle to return on assertion (default credential's)
	Flags      *protocol.AuthenticatorFlags
	Cred       *Credential       // credential to assert with (default the first matching RP)
	SignWith   *ecdsa.PrivateKey // signing key (default the credential's)
}

type optionsEnvelope struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		RP        struct {
			ID string `json:"id"`
		} `json:"rp"`
		RPID string `json:"rpId"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	} `json:"publicKey"`
}

func parseOptions(options []byte) (optionsEnvelope, error) {
	var env optionsEnvelope
	if err := json.Unmarshal(options, &env); err != nil {
		return env, fmt.Errorf("webauthntest: options: %w", err)
	}
	if env.PublicKey.Challenge == "" {
		return env, fmt.Errorf("webauthntest: options carry no challenge")
	}
	return env, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (a *Authenticator) flags(extra protocol.AuthenticatorFlags) protocol.AuthenticatorFlags {
	f := protocol.FlagUserPresent | extra
	if a.UV {
		f |= protocol.FlagUserVerified
	}
	if a.BE {
		f |= protocol.FlagBackupEligible
	}
	if a.BS {
		f |= protocol.FlagBackupState
	}
	return f
}

func clientData(typ, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}

func authData(rpID string, flags protocol.AuthenticatorFlags, count uint32, attested []byte) []byte {
	h := sha256.Sum256([]byte(rpID))
	d := append([]byte{}, h[:]...)
	d = append(d, byte(flags))
	d = binary.BigEndian.AppendUint32(d, count)
	return append(d, attested...)
}

func sign(key *ecdsa.PrivateKey, ad, cdj []byte) ([]byte, error) {
	ch := sha256.Sum256(cdj)
	digest := sha256.Sum256(append(append([]byte{}, ad...), ch[:]...))
	return ecdsa.SignASN1(rand.Reader, key, digest[:])
}

func cosePublicKey(key *ecdsa.PrivateKey) ([]byte, error) {
	pub, err := key.PublicKey.Bytes() // uncompressed 0x04||X||Y
	if err != nil {
		return nil, err
	}
	return webauthncbor.Marshal(map[int64]any{
		1:  int64(webauthncose.EllipticKey),
		3:  int64(webauthncose.AlgES256),
		-1: int64(webauthncose.P256),
		-2: pub[1:33],
		-3: pub[33:65],
	})
}

// Register answers creation options (the JSON object holding "publicKey") and stores
// the new resident credential. It returns the registration response JSON.
func (a *Authenticator) Register(options []byte, o Overrides) ([]byte, *Credential, error) {
	env, err := parseOptions(options)
	if err != nil {
		return nil, nil, err
	}
	userHandle, err := base64.RawURLEncoding.DecodeString(env.PublicKey.User.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("webauthntest: user.id: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		return nil, nil, err
	}
	rpID := env.PublicKey.RP.ID
	cred := &Credential{ID: id, UserHandle: userHandle, RPID: rpID, Key: key}
	if o.RPID != "" {
		rpID = o.RPID
	}
	pk, err := cosePublicKey(key)
	if err != nil {
		return nil, nil, err
	}
	attested := make([]byte, 16) // zero AAGUID
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(id)))
	attested = append(attested, id...)
	attested = append(attested, pk...)
	flags := a.flags(protocol.FlagAttestedCredentialData)
	if o.Flags != nil {
		flags = *o.Flags
	}
	var count uint32
	if o.SignCount != nil {
		count = *o.SignCount
	}
	ad := authData(rpID, flags, count, attested)
	attObj, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": ad})
	if err != nil {
		return nil, nil, err
	}
	cdj := clientData(def(o.Type, "webauthn.create"), def(o.Challenge, env.PublicKey.Challenge), def(o.Origin, a.Origin))
	body, err := json.Marshal(map[string]any{
		"id": b64(id), "rawId": b64(id), "type": "public-key",
		"authenticatorAttachment": "platform",
		"response": map[string]any{
			"attestationObject": b64(attObj),
			"clientDataJSON":    b64(cdj),
			"transports":        []string{"internal"},
		},
		"clientExtensionResults": map[string]any{},
	})
	if err != nil {
		return nil, nil, err
	}
	cred.SignCount = count
	a.Creds = append(a.Creds, cred)
	return body, cred, nil
}

// Assert answers request options with an assertion from a resident credential.
func (a *Authenticator) Assert(options []byte, o Overrides) ([]byte, error) {
	env, err := parseOptions(options)
	if err != nil {
		return nil, err
	}
	rpID := env.PublicKey.RPID
	cred := o.Cred
	if cred == nil {
		for _, c := range a.Creds {
			if c.RPID == rpID {
				cred = c
				break
			}
		}
	}
	if cred == nil {
		return nil, fmt.Errorf("webauthntest: no credential for rp %q", rpID)
	}
	if o.RPID != "" {
		rpID = o.RPID
	}
	count := cred.SignCount + 1
	if o.SignCount != nil {
		count = *o.SignCount
	}
	flags := a.flags(0)
	if o.Flags != nil {
		flags = *o.Flags
	}
	ad := authData(rpID, flags, count, nil)
	cdj := clientData(def(o.Type, "webauthn.get"), def(o.Challenge, env.PublicKey.Challenge), def(o.Origin, a.Origin))
	key := cred.Key
	if o.SignWith != nil {
		key = o.SignWith
	}
	sig, err := sign(key, ad, cdj)
	if err != nil {
		return nil, err
	}
	uh := cred.UserHandle
	if o.UserHandle != nil {
		uh = o.UserHandle
	}
	if o.SignCount == nil || *o.SignCount > cred.SignCount {
		cred.SignCount = count
	}
	return json.Marshal(map[string]any{
		"id": b64(cred.ID), "rawId": b64(cred.ID), "type": "public-key",
		"authenticatorAttachment": "platform",
		"response": map[string]any{
			"authenticatorData": b64(ad),
			"clientDataJSON":    b64(cdj),
			"signature":         b64(sig),
			"userHandle":        b64(uh),
		},
		"clientExtensionResults": map[string]any{},
	})
}

func def(v, d string) string {
	if v != "" {
		return v
	}
	return d
}
