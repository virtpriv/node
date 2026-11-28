// internal/logger/logger.go

// Package logger provides structured logging for the vpn application.
//
// Log file: paths.LogFile. The vpn program creates it when it opens it, and
// the install gives it to the admin user when it finishes.
//
// Log format, one line for each entry:
//
//	[2026-02-21 14:30:05 UTC] [section] message
//
// Sections:
//
//	[install]  installation steps and progress
//	[verify]   GPG signature and checksum verification
//	[tui]      TUI actions (updates, restarts, release checks)
//	[system]   system-level operations (firewall, tor, services)
package logger

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/virtpriv/node/internal/paths"
)

var (
	mu  sync.Mutex
	out io.Writer
)

// Open sends the log to paths.LogFile. Only the vpn program calls it, first
// thing, so a tool or test that shares vpn's code never writes the node's
// log. Until then, and when the file cannot be opened, lines go nowhere.
func Open() {
	f, err := os.OpenFile(paths.LogFile,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err == nil {
		SetOutput(f)
	}
}

func Log(section, format string, args ...interface{}) {
	mu.Lock()
	defer mu.Unlock()
	if out == nil {
		return
	}
	fmt.Fprintf(out, "[%s] [%s] %s\n",
		time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
		section,
		oneLine(fmt.Sprintf(format, args...)))
}

// oneLine keeps a message on one line, so every line of the log starts with
// its time. Paragraphs become sentences. Any other line break, control
// character or invisible format character becomes a space, as theme.PlainText
// does for the screens, so a program's own output cannot control a terminal
// that shows the log or disguise the order of what it shows.
func oneLine(msg string) string {
	var paras []string
	for _, p := range strings.Split(msg, "\n\n") {
		var lines []string
		for _, l := range strings.Split(p, "\n") {
			if l = strings.TrimSpace(strings.Map(unsafeToSpace, l)); l != "" {
				lines = append(lines, l)
			}
		}
		if len(lines) > 0 {
			paras = append(paras, strings.Join(lines, " "))
		}
	}
	for i, p := range paras {
		if i > 0 {
			r, n := utf8.DecodeRuneInString(p)
			p = string(unicode.ToUpper(r)) + p[n:]
		}
		if i < len(paras)-1 && !strings.HasSuffix(p, ".") {
			p += "."
		}
		paras[i] = p
	}
	return strings.Join(paras, " ")
}

func unsafeToSpace(r rune) rune {
	if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
		return ' '
	}
	return r
}

// SetOutput sends the log to w and returns where it went before. Tests use
// it to read what was logged.
func SetOutput(w io.Writer) io.Writer {
	mu.Lock()
	defer mu.Unlock()
	prev := out
	out = w
	return prev
}

func Install(format string, args ...interface{}) {
	Log("install", format, args...)
}

func Verify(format string, args ...interface{}) {
	Log("verify", format, args...)
}

func TUI(format string, args ...interface{}) {
	Log("tui", format, args...)
}

func System(format string, args ...interface{}) {
	Log("system", format, args...)
}
