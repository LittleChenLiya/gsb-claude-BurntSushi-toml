package toml

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// Edit describes a single scalar replacement in a [Document].
//
// Path is a list of TOML key segments:
//
//   - Each segment is one already-unquoted TOML key. A dotted quoted key such
//     as a."b.c".d is the three segments ["a", "b.c", "d"]; dots inside a
//     quoted key are not split into a hierarchy.
//   - Elements of an array of tables ([[name]]) and elements of an ordinary
//     array are addressed by their explicit occurrence index as a decimal
//     string. For example ["servers", "1", "port"] is the port of the second
//     [[servers]] table.
//
// Only existing scalar values, and scalar elements of ordinary arrays, can be
// replaced. Keys, tables, and arrays-of-tables are never added, removed, or
// reordered. Every byte outside the replaced token ranges (comments, blank
// lines, indentation, quoting style, number bases, and line endings such as
// CRLF) is preserved exactly.
//
// The value is encoded with the same rules as [Encoder]; the scalar type may
// change, but the encoded fragment must parse back to a valid TOML scalar.
type Edit struct {
	Path  []string
	Value any
}

// EditResult reports one applied [Edit].
//
// Position is the byte range of the replaced token in the original document
// and Line/Col point at its first byte. Old and New are the original and the
// replacement TOML source text.
type EditResult struct {
	Path     Key
	Position Position
	Old      string
	New      string
}

// EditError reports a rejected [Edit]. Line and Col identify the target token
// when the path resolved to one; they are 0 otherwise.
//
// Index is the position of the edit in the supplied batch.
type EditError struct {
	Index int
	Path  Key
	Line  int
	Col   int
	Msg   string
}

func (e *EditError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "toml: edit #%d", e.Index)
	if len(e.Path) > 0 {
		fmt.Fprintf(&b, " (%s)", e.Path)
	}
	b.WriteString(": ")
	if e.Line > 0 {
		fmt.Fprintf(&b, "line %d, column %d: ", e.Line, e.Col)
	}
	b.WriteString(e.Msg)
	return b.String()
}

// Document is an editable TOML document.
//
// It records the exact byte range of every scalar token while parsing, so
// edits splice encoded values into the original bytes instead of re-encoding
// the whole document:
//
//	doc, err := toml.ParseDocument(data)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	doc.Set([]string{"servers", "1", "port"}, 8080)
//	doc.Set([]string{"title"}, "New title")
//	results, err := doc.Apply() // all edits are validated as one batch
//	if err != nil {
//	    log.Fatal(err) // nothing was modified
//	}
//	if _, err := doc.WriteTo(os.Stdout); err != nil {
//	    log.Fatal(err)
//	}
//
// Parse errors, invalid values, unknown paths, out-of-range indices, and
// duplicate or overlapping target ranges reject the whole batch before the
// document or any output file is modified.
type Document struct {
	data    []byte // Document bytes, including any leading BOM.
	bom     []byte // Leading BOM, if present.
	input   string // Document without the BOM; token offsets index into this.
	mapping map[string]any
	recs    []spanRec
	pending []Edit
}

type spanRec struct {
	path  Key
	start int // Byte offset in d.input.
	end   int
}

// ParseDocument parses data and returns an editable [Document]. A document
// containing TOML syntax errors can not be edited.
func ParseDocument(data []byte) (*Document, error) {
	bom, input := stripBOM(string(data))
	p, err := parse(input, 128)
	if err != nil {
		return nil, err
	}
	recs, err := scanSpans(input, p)
	if err != nil {
		return nil, err
	}
	return &Document{
		data:    append(append([]byte(nil), bom...), input...),
		bom:     bom,
		input:   input,
		mapping: p.mapping,
		recs:    recs,
	}, nil
}

// Set queues an edit replacing the scalar at path with value. Edits are not
// applied until [Document.Apply] runs and validates the whole batch. Set
// returns the document to allow chaining.
func (d *Document) Set(path []string, value any) *Document {
	d.pending = append(d.pending, Edit{Path: append(Key(nil), path...), Value: value})
	return d
}

// Apply validates all queued edits as one batch and splices them into the
// document bytes.
//
// It returns one [EditResult] per applied edit, in queue order. If any edit is
// invalid, the document is left untouched and all problems are reported
// (joined with errors.Join).
func (d *Document) Apply() ([]EditResult, error) {
	edits := d.pending
	if len(edits) == 0 {
		return nil, nil
	}

	type planned struct {
		i    int
		path Key
		rec  spanRec
		frag string
		want any
		line int
		col  int
	}
	var (
		plans []planned
		errs  []error
	)
	addErr := func(i int, path Key, line, col int, format string, args ...any) {
		errs = append(errs, &EditError{
			Index: i,
			Path:  path,
			Line:  line,
			Col:   col,
			Msg:   fmt.Sprintf(format, args...),
		})
	}

	for i, ed := range edits {
		path := Key(ed.Path)
		if len(path) == 0 {
			addErr(i, path, 0, 0, "empty key path")
			continue
		}
		node, line, col, ok := d.resolve(path, addErr, i)
		if !ok {
			continue
		}
		switch node.(type) {
		case map[string]any:
			addErr(i, path, line, col, "cannot replace a table; only scalar values can be replaced")
			continue
		case []map[string]any:
			addErr(i, path, line, col, "cannot replace an array of tables; index one of its elements")
			continue
		case []any:
			addErr(i, path, line, col, "cannot replace an entire array; index one of its scalar elements")
			continue
		}
		frag, want, ferr := encodeScalar(ed.Value)
		if ferr != nil {
			addErr(i, path, line, col, "invalid TOML scalar for %T: %v", ed.Value, ferr)
			continue
		}
		if strings.ContainsAny(frag, "\r\n") {
			addErr(i, path, line, col, "encoded value must be a single-line scalar")
			continue
		}

		rec, found := findRec(d.recs, path)
		if !found {
			addErr(i, path, line, col, "internal error: no token recorded for path")
			continue
		}
		plans = append(plans, planned{
			i: i, path: path, rec: rec, frag: frag, want: want, line: line, col: col,
		})
	}

	// Reject duplicate and overlapping token ranges across the whole batch.
	for i := 0; i < len(plans); i++ {
		for j := i + 1; j < len(plans); j++ {
			a, b := plans[i].rec, plans[j].rec
			if a.start < b.end && b.start < a.end {
				if a.start == b.start && a.end == b.end {
					addErr(plans[j].i, plans[j].path, plans[j].line, plans[j].col,
						"duplicate edit for the same token range as edit #%d", plans[i].i)
				} else {
					addErr(plans[j].i, plans[j].path, plans[j].line, plans[j].col,
						"token range overlaps with edit #%d", plans[i].i)
				}
			}
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	// Splice from the end so earlier offsets remain valid.
	ordered := make([]planned, len(plans))
	copy(ordered, plans)
	for i := 0; i < len(ordered); i++ {
		for j := i + 1; j < len(ordered); j++ {
			if ordered[j].rec.start > ordered[i].rec.start {
				ordered[i], ordered[j] = ordered[j], ordered[i]
			}
		}
	}
	out := d.input
	for _, pl := range ordered {
		out = out[:pl.rec.start] + pl.frag + out[pl.rec.end:]
	}

	// Re-parse and verify every new scalar decodes back to the requested value.
	p, perr := parse(out, 128)
	if perr != nil {
		return nil, fmt.Errorf("toml: document is invalid after applying edits: %w", perr)
	}
	for _, pl := range plans {
		got, ok := navigate(p.mapping, pl.path)
		if !ok {
			addErr(pl.i, pl.path, pl.line, pl.col, "path missing after edit")
			continue
		}
		if !scalarEqual(got, pl.want) {
			addErr(pl.i, pl.path, pl.line, pl.col,
				"encoded value %q does not decode back to the requested value", pl.frag)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	recs, serr := scanSpans(out, p)
	if serr != nil {
		return nil, serr
	}

	results := make([]EditResult, len(plans))
	for i, pl := range plans {
		results[i] = EditResult{
			Path: pl.path,
			Old:  d.input[pl.rec.start:pl.rec.end],
			New:  pl.frag,
			Position: Position{
				Line:  pl.line,
				Col:   pl.col,
				Start: pl.rec.start + len(d.bom),
				Len:   pl.rec.end - pl.rec.start,
			},
		}
	}

	d.input = out
	d.data = append(append(make([]byte, 0, len(d.bom)+len(out)), d.bom...), out...)
	d.mapping = p.mapping
	d.recs = recs
	d.pending = nil
	return results, nil
}

// resolve walks the parsed mapping for path, collecting errors for unknown
// keys and out-of-range indices. It returns the target node and the recorded
// location of the deepest existing path token.
func (d *Document) resolve(path Key, addErr func(int, Key, int, int, string, ...any), i int) (any, int, int, bool) {
	var (
		node     any = d.mapping
		line, col     = 0, 0
	)
	for si, seg := range path {
		if rec, ok := findRec(d.recs, path[:si+1]); ok {
			line, col = lineCol(d.input, rec.start)
		}
		switch n := node.(type) {
		case map[string]any:
			v, ok := n[seg]
			if !ok {
				addErr(i, path, line, col, "unknown path: key %q does not exist", seg)
				return nil, 0, 0, false
			}
			node = v
		case []map[string]any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 {
				addErr(i, path, line, col, "unknown path: %q is not an array-of-tables index", seg)
				return nil, 0, 0, false
			}
			if idx >= len(n) {
				addErr(i, path, line, col, "array-of-tables index %d out of range (length %d)", idx, len(n))
				return nil, 0, 0, false
			}
			node = n[idx]
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 {
				addErr(i, path, line, col, "unknown path: %q is not an array index", seg)
				return nil, 0, 0, false
			}
			if idx >= len(n) {
				addErr(i, path, line, col, "array index %d out of range (length %d)", idx, len(n))
				return nil, 0, 0, false
			}
			node = n[idx]
		default:
			addErr(i, path, line, col, "unknown path: %q is below a scalar value", seg)
			return nil, 0, 0, false
		}
	}
	return node, line, col, true
}

// Bytes returns a copy of the current document bytes, including any leading
// BOM.
func (d *Document) Bytes() []byte {
	out := make([]byte, len(d.data))
	copy(out, d.data)
	return out
}

// WriteTo applies any pending edits and writes the document to w. It
// implements io.WriterTo. Nothing is written when the pending edits fail
// validation.
func (d *Document) WriteTo(w io.Writer) (int64, error) {
	if len(d.pending) > 0 {
		if _, err := d.Apply(); err != nil {
			return 0, err
		}
	}
	n, err := w.Write(d.data)
	return int64(n), err
}

// WriteFile applies any pending edits and writes the document to path. The
// file is created or truncated only after every edit has been validated, so a
// failed batch never creates or truncates the file.
func (d *Document) WriteFile(path string) ([]EditResult, error) {
	results, err := d.Apply()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, d.data, 0o644); err != nil {
		return nil, err
	}
	return results, nil
}

// Edit applies edits to data and returns the modified document bytes along
// with one [EditResult] per applied edit. On failure no edits are applied and
// nil bytes are returned.
func Edit(data []byte, edits ...Edit) ([]byte, []EditResult, error) {
	d, err := ParseDocument(data)
	if err != nil {
		return nil, nil, err
	}
	for _, ed := range edits {
		d.Set(ed.Path, ed.Value)
	}
	results, err := d.Apply()
	if err != nil {
		return nil, nil, err
	}
	return d.Bytes(), results, nil
}

// EditFile reads the TOML file at path, applies edits, and writes the result
// back only after the whole batch validates. A failed batch never creates or
// truncates the file.
func EditFile(path string, edits ...Edit) ([]EditResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	d, err := ParseDocument(data)
	if err != nil {
		return nil, err
	}
	for _, ed := range edits {
		d.Set(ed.Path, ed.Value)
	}
	return d.WriteFile(path)
}

// encodeScalar encodes v to a single TOML scalar fragment by reusing [Marshal]
// under a throwaway "k" key, then parses the fragment to prove it is exactly
// one scalar and to obtain the value the decoder reads back.
func encodeScalar(v any) (frag string, decoded any, err error) {
	if v == nil {
		return "", nil, fmt.Errorf("nil is not a TOML scalar")
	}
	encoded, err := Marshal(map[string]any{"k": v})
	if err != nil {
		return "", nil, err
	}
	const prefix = "k = "
	if !strings.HasPrefix(encoded, prefix) {
		return "", nil, fmt.Errorf("internal error: unexpected encoder output %q", encoded)
	}
	frag = strings.TrimSuffix(string(encoded), "\n")[len(prefix):]

	// Prove the fragment is exactly one scalar: a synthetic "k = <frag>"
	// document must parse, contain a scalar "k", and the recorded token for k
	// must cover the whole fragment (no trailing comments or other values).
	synthetic := prefix + frag + "\n"
	p, perr := parse(synthetic, 128)
	if perr != nil {
		return "", nil, fmt.Errorf("value does not encode to valid TOML: %w", perr)
	}
	got, ok := p.mapping["k"]
	if !ok || !isScalarValue(got) {
		return "", nil, fmt.Errorf("value encodes to non-scalar TOML (%T)", v)
	}
	recs, serr := scanSpans(synthetic, p)
	if serr != nil {
		return "", nil, serr
	}
	rec, found := findRec(recs, Key{"k"})
	if !found || rec.start != len(prefix) || rec.end != len(synthetic)-1 {
		return "", nil, fmt.Errorf("value encodes to non-scalar TOML (%T)", v)
	}
	return frag, got, nil
}

func isScalarValue(v any) bool {
	switch v.(type) {
	case map[string]any, []map[string]any, []any:
		return false
	default:
		return v != nil
	}
}

// scalarEqual reports whether a decoded value is equal to the value expected
// from an edit, with time and NaN semantics that reflect TOML semantics.
func scalarEqual(got, want any) bool {
	if gt, ok := got.(time.Time); ok {
		if wt, ok := want.(time.Time); ok {
			return gt.Equal(wt)
		}
		return false
	}
	if gf, ok := got.(float64); ok {
		if wf, ok := want.(float64); ok {
			return gf == wf || (math.IsNaN(gf) && math.IsNaN(wf))
		}
		return false
	}
	return reflect.DeepEqual(got, want)
}

// navigate walks the parsed mapping; string segments name map keys, and
// decimal segments index []any and []map[string]any nodes.
func navigate(mapping map[string]any, path Key) (any, bool) {
	var node any = mapping
	for _, seg := range path {
		switch n := node.(type) {
		case map[string]any:
			v, ok := n[seg]
			if !ok {
				return nil, false
			}
			node = v
		case []map[string]any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(n) {
				return nil, false
			}
			node = n[idx]
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(n) {
				return nil, false
			}
			node = n[idx]
		default:
			return nil, false
		}
	}
	return node, true
}

func findRec(recs []spanRec, path Key) (spanRec, bool) {
	for _, r := range recs {
		if keysEqual(r.path, path) {
			return r, true
		}
	}
	return spanRec{}, false
}

func keysEqual(a, b Key) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// lineCol converts a byte offset to a 1-based line and column in input. A bare
// \r (not part of CRLF) never occurs in documents that passed parsing.
func lineCol(input string, offset int) (line, col int) {
	if offset > len(input) {
		offset = len(input)
	}
	line, col = 1, 1
	for i := 0; i < offset; i++ {
		if input[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

// stripBOM removes a leading Unicode BOM, matching the treatment in parse().
func stripBOM(data string) (bom, rest []byte) {
	switch {
	case strings.HasPrefix(data, "\ufffe") || strings.HasPrefix(data, "\ufeff"):
		// UTF-16 BOMs are rejected by parse() as NULL bytes; only handle the
		// UTF-8 BOM here, and leave the rest for parse() to reject.
	}
	if strings.HasPrefix(data, "\ufeff") {
		return []byte(data[:3]), []byte(data[3:])
	}
	return nil, []byte(data)
}

