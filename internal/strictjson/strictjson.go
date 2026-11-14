// Package strictjson decodes JSON and refuses anything it was not told to
// expect: bytes that are not UTF-8, unknown fields, and data after the value.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// ErrNotStrict is returned for every refusal. The cause is deliberately not
// carried: callers map it to a public rule, and nothing else may leak out.
var ErrNotStrict = errors.New("strictjson: input refused")

// Decode reads exactly one JSON value from data into v.
func Decode(data []byte, v any) error {
	if !utf8.Valid(data) {
		return ErrNotStrict
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return ErrNotStrict
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrNotStrict
	}
	return nil
}
