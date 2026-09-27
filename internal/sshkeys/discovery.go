package sshkeys

import "strings"

// Source contains only supported public keys. Problem distinguishes an
// incomplete observation from a successfully read source with no supported keys.
type Source struct {
	User     string `json:"user"`
	Path     string `json:"path"`
	Keys     []Key  `json:"keys"`
	Excluded int    `json:"excluded"`
	Problem  string `json:"problem,omitempty"`
}

// Classify excludes restricted, malformed and obsolete key entries rather than
// turning a restricted credential into an unrestricted one by removing options.
func Classify(content string) ([]Key, int) {
	var keys []Key
	excluded := 0
	for _, line := range strings.Split(content, "\n") {
		line = strings.Trim(line, " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, err := Parse(line)
		if err == nil {
			keys = append(keys, key)
			continue
		}
		for _, field := range strings.Fields(line) {
			if RecognizedType(field) || strings.Contains(field, "-cert-v01@openssh.com") {
				excluded++
				break
			}
		}
	}
	return keys, excluded
}
