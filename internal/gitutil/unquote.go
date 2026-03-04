// Package gitutil provides utilities for handling git-specific encoding quirks.
package gitutil

import (
	"strconv"
	"strings"
)

// UnquoteGitPath decodes a git C-style quoted path string.
// When core.quotePath=true (git default), non-ASCII bytes are escaped as
// octal sequences (e.g., \343\205\201) inside double-quoted strings.
// This function reverses that encoding.
//
// If the input is not quoted, it is returned as-is.
func UnquoteGitPath(s string) string {
	// Git only quotes paths that need escaping — they always start and end with "
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}

	// Strip surrounding quotes
	inner := s[1 : len(s)-1]

	var buf strings.Builder
	buf.Grow(len(inner))

	i := 0
	for i < len(inner) {
		if inner[i] != '\\' {
			buf.WriteByte(inner[i])
			i++
			continue
		}

		// Escape sequence
		i++ // skip backslash
		if i >= len(inner) {
			buf.WriteByte('\\')
			break
		}

		switch inner[i] {
		case '\\':
			buf.WriteByte('\\')
			i++
		case '"':
			buf.WriteByte('"')
			i++
		case 'n':
			buf.WriteByte('\n')
			i++
		case 't':
			buf.WriteByte('\t')
			i++
		case 'a':
			buf.WriteByte('\a')
			i++
		case 'b':
			buf.WriteByte('\b')
			i++
		case 'r':
			buf.WriteByte('\r')
			i++
		case 'f':
			buf.WriteByte('\f')
			i++
		case 'v':
			buf.WriteByte('\v')
			i++
		default:
			// Octal escape: \NNN (1-3 octal digits)
			if inner[i] >= '0' && inner[i] <= '7' {
				end := i + 1
				for end < len(inner) && end < i+3 && inner[end] >= '0' && inner[end] <= '7' {
					end++
				}
				val, err := strconv.ParseUint(inner[i:end], 8, 8)
				if err == nil {
					buf.WriteByte(byte(val))
					i = end
				} else {
					buf.WriteByte('\\')
					buf.WriteByte(inner[i])
					i++
				}
			} else {
				// Unknown escape — preserve literally
				buf.WriteByte('\\')
				buf.WriteByte(inner[i])
				i++
			}
		}
	}

	return buf.String()
}
