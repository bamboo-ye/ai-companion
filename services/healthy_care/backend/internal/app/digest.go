package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func canonical(v any) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	var normalized any
	_ = json.Unmarshal(b.Bytes(), &normalized)
	b.Reset()
	e = json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(normalized)
	return bytes.TrimSpace(b.Bytes())
}
func digest(v any) string { s := sha256.Sum256(canonical(v)); return hex.EncodeToString(s[:]) }
