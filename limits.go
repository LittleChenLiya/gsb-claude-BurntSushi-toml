package toml

import (
	"context"
	"fmt"
	"io"
)

// readChunkSize is the fixed upper bound for the buffer size of a single Read
// request made to the underlying io.Reader. The decoder never asks the reader
// for more than this many bytes in one call, regardless of any configured
// limits.
const readChunkSize = 32 * 1024

// ResourceLimits is a set of optional resource quotas for [Decoder]. All
// limits are disabled by default (the zero value); set them with
// [Decoder.SetResourceLimits].
//
// The limits are enforced while the input is being read, lexed, and parsed:
// decoding stops at the first position that exceeds a limit rather than after
// the entire input has been read or decoded. A value that is exactly equal to
// the limit is still accepted; only the first position *beyond* the limit is
// reported as an error.
//
// The counting rules for each limit are:
//
//   - MaxInputBytes: number of raw bytes read from the underlying io.Reader,
//     before any decoding. Every byte of the TOML document counts, including
//     whitespace, comments, and a BOM.
//
//   - MaxKeyAssignments: number of key/value assignments ("key = value"),
//     including assignments inside inline tables. Table headers ([table] and
//     [[array-of-tables]]) do NOT count as assignments.
//
//   - MaxStringBytes: number of UTF-8 bytes of a single decoded string value
//     (or key). Escapes are counted after expansion (e.g. "\n" counts as 1
//     byte, "\u00e9" as 2). For multiline strings the first newline and any
//     whitespace trimmed by line-ending backslashes (per the TOML spec) are
//     removed before counting.
//
//   - MaxArrayElements: total number of array elements across all arrays in
//     the document. A nested container (array or inline table) inside an
//     array counts as a single element of its parent array, and the elements
//     of a nested array count towards the total as well. Every entry of an
//     [[array-of-tables]] counts as one element.
//
// When several limits (including [Decoder.MaxDepth]) would be exceeded by the
// same document, whichever condition is encountered first while reading and
// parsing is the one that is reported.
//
// When a limit is exceeded decoding stops immediately: no further Read calls
// are made on the underlying reader and the value passed to [Decoder.Decode]
// is left completely unmodified.
type ResourceLimits struct {
	MaxInputBytes     int64 // Raw bytes read from the io.Reader; 0 = unlimited.
	MaxKeyAssignments int64 // Number of "key = value" assignments; 0 = unlimited.
	MaxStringBytes    int64 // UTF-8 bytes of a single decoded string; 0 = unlimited.
	MaxArrayElements  int64 // Total array elements in the document; 0 = unlimited.
}

func (l ResourceLimits) validate() error {
	for _, lim := range []struct {
		name string
		v    int64
	}{
		{"MaxInputBytes", l.MaxInputBytes},
		{"MaxKeyAssignments", l.MaxKeyAssignments},
		{"MaxStringBytes", l.MaxStringBytes},
		{"MaxArrayElements", l.MaxArrayElements},
	} {
		if lim.v < 0 {
			return fmt.Errorf("toml: resource limit %s cannot be negative: %d", lim.name, lim.v)
		}
	}
	return nil
}

// ResourceLimitError is returned (possibly wrapped in a [ParseError]) when a
// resource limit set with [Decoder.SetResourceLimits] is exceeded. Use
// errors.As to retrieve it:
//
//	var rlErr toml.ResourceLimitError
//	if errors.As(err, &rlErr) { ... }
type ResourceLimitError struct {
	Name  string // Name of the exceeded limit, e.g. "MaxInputBytes".
	Limit int64  // Configured limit that was exceeded.
	Line  int    // Line of the first position beyond the limit, starting at 1.
	Col   int    // Column of the first position beyond the limit, starting at 1.
}

func (e ResourceLimitError) Error() string {
	return fmt.Sprintf("toml: resource limit %s of %d exceeded at line %d, column %d",
		e.Name, e.Limit, e.Line, e.Col)
}

// readInput reads the entire TOML document from the underlying reader,
// enforcing MaxInputBytes and context cancellation as it reads.
//
// Every Read request is bounded to readChunkSize bytes (or fewer when the
// remaining MaxInputBytes budget is smaller), and reading stops as soon as a
// limit is exceeded or the context is cancelled: no further Read calls are
// made after an error is returned.
//
// Errors returned by the underlying reader are returned unmodified so callers
// can recognise them with errors.Is/errors.As.
func (dec *Decoder) readInput() ([]byte, error) {
	ctx := dec.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	lim := dec.limits.MaxInputBytes

	var (
		buf   []byte
		total int64
		line  = 1
		col   = 0
	)
	chunk := make([]byte, readChunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req := int64(readChunkSize)
		if lim > 0 {
			// Never request more than the remaining budget plus one byte;
			// that single byte tells us whether the limit is exceeded.
			if rem := lim - total + 1; rem < req {
				req = rem
			}
		}
		n, err := dec.r.Read(chunk[:req])
		if n > 0 {
			if lim > 0 && total+int64(n) > lim {
				// The first byte beyond the limit is the position of the
				// error; count lines/columns up to and including it.
				over := chunk[:lim-total+1]
				for _, b := range over {
					if b == '\n' {
						line++
						col = 0
					} else {
						col++
					}
				}
				return nil, ResourceLimitError{
					Name:  "MaxInputBytes",
					Limit: lim,
					Line:  line,
					Col:   col,
				}
			}
			for _, b := range chunk[:n] {
				if b == '\n' {
					line++
					col = 0
				} else {
					col++
				}
			}
			buf = append(buf, chunk[:n]...)
			total += int64(n)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
	}
	return buf, nil
}
