// Package passkeytest provides a software ES256 authenticator for protocol tests.
// It signs actual WebAuthn challenges; production code must never import it.
package passkeytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

type Authenticator struct {
	Key     *ecdsa.PrivateKey
	ID      []byte
	Handle  string
	Counter uint32
}

func New(t testing.TB) *Authenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	if _, err = rand.Read(id); err != nil {
		t.Fatal(err)
	}
	return &Authenticator{Key: key, ID: id}
}
func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func JSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type options struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		User      struct {
			ID string `json:"id"`
		} `json:"user"`
	} `json:"publicKey"`
}

func (a *Authenticator) Registration(t testing.TB, opts any, origin, rpID string, uv bool) []byte {
	t.Helper()
	var o options
	if err := json.Unmarshal(JSON(t, opts), &o); err != nil {
		t.Fatal(err)
	}
	a.Handle = o.PublicKey.User.ID
	client := JSON(t, map[string]any{"type": "webauthn.create", "challenge": o.PublicKey.Challenge, "origin": origin, "crossOrigin": false})
	flags := byte(0x41)
	if uv {
		flags |= 0x04
	}
	hash := sha256.Sum256([]byte(rpID))
	data := append(hash[:], flags, 0, 0, 0, 0)
	data = append(data, make([]byte, 16)...)
	data = binary.BigEndian.AppendUint16(data, uint16(len(a.ID)))
	data = append(data, a.ID...)
	key, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: a.Key.X.FillBytes(make([]byte, 32)), -3: a.Key.Y.FillBytes(make([]byte, 32))})
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, key...)
	attestation, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": data})
	if err != nil {
		t.Fatal(err)
	}
	return JSON(t, map[string]any{"id": b64(a.ID), "rawId": b64(a.ID), "type": "public-key", "clientExtensionResults": map[string]any{}, "response": map[string]any{"clientDataJSON": b64(client), "attestationObject": b64(attestation), "transports": []string{"internal"}}})
}
func (a *Authenticator) Assertion(t testing.TB, opts any, origin, rpID string, uv bool) []byte {
	t.Helper()
	var o options
	if err := json.Unmarshal(JSON(t, opts), &o); err != nil {
		t.Fatal(err)
	}
	client := JSON(t, map[string]any{"type": "webauthn.get", "challenge": o.PublicKey.Challenge, "origin": origin, "crossOrigin": false})
	flags := byte(1)
	if uv {
		flags |= 4
	}
	a.Counter++
	hash := sha256.Sum256([]byte(rpID))
	data := append(hash[:], flags)
	data = binary.BigEndian.AppendUint32(data, a.Counter)
	clientHash := sha256.Sum256(client)
	signed := append(append([]byte{}, data...), clientHash[:]...)
	digest := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, a.Key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return JSON(t, map[string]any{"id": b64(a.ID), "rawId": b64(a.ID), "type": "public-key", "clientExtensionResults": map[string]any{}, "response": map[string]any{"clientDataJSON": b64(client), "authenticatorData": b64(data), "signature": b64(sig), "userHandle": a.Handle}})
}
