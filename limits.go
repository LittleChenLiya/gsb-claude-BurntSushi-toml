package toml

import (
	"fmt"
	"strconv"
	"strings"
)

// LimitError is returned when one of the optional [Decoder] resource limits
// is exceeded. It is wrapped in a [ParseError], which carries the line and
// column of the first position that exceeds the limit.
type LimitError struct {
	Name  string // Name of the limit, e.g. "MaxInputBytes".
	Limit int64  // The configured limit that was exceeded.
}

func (e LimitError) Error() string {
	return fmt.Sprintf("resource limit %s exceeded: limit is %d", e.Name, e.Limit)
}

// limits holds the optional [Decoder] resource limits; a zero value disables
// the corresponding limit.
type limits struct {
	maxInputBytes int64
	maxKeyAssign  int
	maxString     int
	maxArrayElem  int
}

// errRead wraps an error from the input reader or from context cancellation,
// so the parser can tell it apart from TOML syntax errors and return it
// unwrapped (keeping the underlying error identifiable with errors.Is/As).
type errRead struct{ err error }

func (e errRead) Error() string { return e.err.Error() }
func (e errRead) Unwrap() error { return e.err }

// stringExceedOffset returns the byte offset in the raw token it.val at which
// the decoded string would grow beyond max decoded bytes, or -1 if the
// decoded string fits within the limit.
//
// The decoded size is the number of UTF-8 bytes after escape expansion; for
// multiline strings the whitespace the TOML spec strips (the first newline
// and whitespace after line-ending backslashes) is removed before counting.
func stringExceedOffset(it item, max int) int {
	s := it.val
	i := 0
	if it.typ == itemMultilineString || it.typ == itemRawMultilineString {
		i = len(s) - len(stripFirstNewline(s))
	}
	switch it.typ {
	case itemString, itemRawString, itemRawMultilineString:
		// No escapes: decoded size is the raw size.
		if len(s)-i > max {
			return i + max
		}
		return -1
	}

	count := 0
	for i < len(s) {
		if s[i] != '\\' {
			count++
			if count > max {
				return i
			}
			i++
			continue
		}

		// A line-ending backslash in a multiline string strips itself and all
		// whitespace up to the next non-whitespace character.
		if it.typ == itemMultilineString {
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\r' || s[j] == '\n') {
				j++
			}
			if j > i+1 && strings.Contains(s[i:j], "\n") {
				i = j
				continue
			}
		}

		if i+1 >= len(s) {
			// Invalid escape; let the parser's escape handling report it.
			return -1
		}
		var n, adv int
		switch s[i+1] {
		case 'x':
			n, adv = runeUTF8Len(hexVal(s, i+2, 2)), 4
		case 'u':
			n, adv = runeUTF8Len(hexVal(s, i+2, 4)), 6
		case 'U':
			n, adv = runeUTF8Len(hexVal(s, i+2, 8)), 10
		default: // \b \t \n \f \r \e \" \\ and invalid escapes
			n, adv = 1, 2
		}
		count += n
		if count > max {
			return i
		}
		i += adv
	}
	return -1
}

// hexVal parses up to n hexadecimal digits of s starting at i; invalid input
// (which the parser will report later) yields 0.
func hexVal(s string, i, n int) int64 {
	if i+n > len(s) {
		return 0
	}
	v, err := strconv.ParseInt(s[i:i+n], 16, 64)
	if err != nil {
		return 0
	}
	return v
}

// runeUTF8Len returns the number of bytes v takes when encoded as UTF-8.
func runeUTF8Len(v int64) int {
	switch {
	case v < 0x80:
		return 1
	case v < 0x800:
		return 2
	case v < 0x10000:
		return 3
	default:
		return 4
	}
}
