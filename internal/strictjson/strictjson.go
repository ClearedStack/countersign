// Package strictjson decodes JSON and refuses anything it was not told to
// expect: bytes that are not UTF-8, unknown fields, and data after the value.
// Object also refuses duplicate keys and deep nesting.
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

// MaxDepth is the deepest nesting of objects and arrays Object accepts. It is
// the depth Waybill accepts.
const MaxDepth = 32

// Object checks that data is exactly one JSON object in UTF-8, with no
// duplicate key at any depth and no more than MaxDepth levels of nesting. It
// returns the object with white space removed and nothing else changed.
func Object(data []byte) ([]byte, error) {
	if !utf8.Valid(data) {
		return nil, ErrNotStrict
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, ErrNotStrict
	}
	dec = json.NewDecoder(bytes.NewReader(data)) // walk reads the opening brace itself
	if err := walk(dec, 0); err != nil {
		return nil, ErrNotStrict
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, ErrNotStrict
	}
	var out bytes.Buffer
	if err := json.Compact(&out, data); err != nil {
		return nil, ErrNotStrict
	}
	return out.Bytes(), nil
}

// walk consumes one value, refusing a repeated key or nesting past MaxDepth.
func walk(dec *json.Decoder, depth int) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if depth >= MaxDepth {
		return ErrNotStrict
	}
	if delim == '{' {
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, _ := key.(string)
			if seen[name] {
				return ErrNotStrict
			}
			seen[name] = true
			if err := walk(dec, depth+1); err != nil {
				return err
			}
		}
	} else {
		for dec.More() {
			if err := walk(dec, depth+1); err != nil {
				return err
			}
		}
	}
	_, err = dec.Token() // the closing delimiter
	return err
}
