// internal/logger/logger.go

// Package logger provides structured logging for the vpn application.
//
// Log file: /var/log/vpn.log (created by the installer, owned by the admin user)
//
// Log format:
//
//	[2026-02-21 14:30:05 UTC] [section] message
//
// Sections:
//
//	[install]  installation steps and progress
//	[verify]   GPG signature and checksum verification
//	[tui]      TUI actions (installs, upgrades, service management)
//	[status]   dashboard status polling warnings
//	[system]   system-level operations (firewall, tor, services)
package logger

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const LogPath = "/var/log/vpn.log"

var (
	mu  sync.Mutex
	out io.Writer
)

func init() {
	// Skip log file initialization during tests.
	// Go test binaries have .test suffix in os.Args[0].
	if len(os.Args) > 0 && strings.HasSuffix(os.Args[0], ".test") {
		return
	}
	f, err := os.OpenFile(LogPath,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err == nil {
		out = f
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
		fmt.Sprintf(format, args...))
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

func Status(format string, args ...interface{}) {
	Log("status", format, args...)
}

func System(format string, args ...interface{}) {
	Log("system", format, args...)
}
