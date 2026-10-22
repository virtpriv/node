// Package loginpassword defines the initial owner login-password contract.
package loginpassword

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MinLength is the minimum password length in characters, the unit the owner
// is told. A character outside ASCII takes more bytes, so this is never weaker
// than counting bytes.
const MinLength = 16

// Password contains a validated secret. Its zero value is invalid.
type Password struct{ text string }

// New validates a password for chpasswd's line-based input protocol.
func New(text string) (Password, error) {
	if text == "" {
		return Password{}, errors.New("password is empty")
	}
	if strings.ContainsAny(text, "\n\x00") {
		return Password{}, errors.New("password contains a newline or NUL")
	}
	if utf8.RuneCountInString(text) < MinLength {
		return Password{}, fmt.Errorf("password must be at least %d characters", MinLength)
	}
	return Password{text: text}, nil
}

// Text exposes the secret only for the installer's password tool stdin.
// It must not be logged or placed in command arguments.
func (p Password) Text() string   { return p.text }
func (Password) String() string   { return "[redacted]" }
func (Password) GoString() string { return "[redacted]" }
