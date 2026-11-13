// Package fault holds the three public error codes and the one error type
// that may leave Countersign.
//
// Messages are fixed text chosen here. They never carry input data, a path,
// an address or a credential, so nothing of the kind can leak through them.
package fault

import (
	"crypto/sha256"
	"encoding/hex"
)

// Code is one of the three public error codes.
type Code string

const (
	// Timeout: something outside took too long.
	Timeout Code = "E_TIMEOUT"
	// Parse: the data was broken.
	Parse Code = "E_PARSE"
	// NeedsSubtool: the data was valid and unfamiliar.
	NeedsSubtool Code = "E_NEEDS_SUBTOOL"
)

// Error is a public error: a code, the name of the rule that refused, and a
// fixed message.
type Error struct {
	Code Code
	Rule string
	Msg  string
}

// New builds a public error.
func New(code Code, rule, msg string) *Error {
	return &Error{Code: code, Rule: rule, Msg: msg}
}

// Fingerprint identifies the kind of failure. It is derived from the code and
// the rule name only, so it is the same on every machine and says nothing
// about the item that failed.
func (e *Error) Fingerprint() string {
	sum := sha256.Sum256([]byte(string(e.Code) + "\x00" + e.Rule))
	return hex.EncodeToString(sum[:6])
}

func (e *Error) Error() string {
	return string(e.Code) + ": " + e.Msg + " (fingerprint " + e.Fingerprint() + ")"
}
