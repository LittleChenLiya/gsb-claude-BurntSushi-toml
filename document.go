package toml

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml/internal"
)

// Document is a parsed TOML document that can be edited in place: scalar
// values are replaced by splicing the newly encoded value over the original
// token, leaving every other byte — comments, whitespace, line endings, and
// the literal form of untouched values — exactly as it was.
//
// The first version only replaces scalars, or scalars inside plain arrays.
// It never adds or removes keys or tables, and never reformats or reorders
// the document.
//
//	doc, err := toml.ParseDocument(data)
//	if err != nil {
//		// The input has parse errors; refuse to edit.
//	}
//	// Key paths are slices of literal key segments; a quoted key such as
//	// "a.b" is a single segment and is never split on the dot.
//	doc.Set([]string{"owner", "name"}, "Tom")
//	// Array-of-tables entries are addressed by an explicit occurrence index.
//	doc.Set([]string{"servers", "1", "port"}, 9001)
//	// So are elements of plain arrays.
//	doc.Set([]string{"ports", "0"}, 8080)
//	out, err := doc.Bytes() // or doc.WriteTo(w)
//
// All edits are validated as a batch before anything is written: unknown
// paths, invalid values, duplicate edits of the same token, and overlapping
// token ranges are rejected and produce no output.
type Document struct {
	data    []byte // Original input, including any BOM.
	bom     int    // Length of a BOM prefix; token ranges are relative to data[bom:].
	mapping map[string]any
	scalars map[string]Position   // Scalar key/value pairs: doc key → token range.
	arrays  map[string][]Position // Plain array elements: doc key → token ranges.
	edits   []docEdit
	err     error // First failed edit, if any; fails the whole batch.
}

type docEdit struct {
	path []string
	rng  Position
	enc  string
}

// EditError describes a failed Document edit. Position holds the line and
// column of the target token when one is known.
type EditError struct {
	Message  string
	Path     []string
	Position Position
}

func (e *EditError) Error() string {
	var b strings.Builder
	b.WriteString("toml: ")
	if e.Position.Line > 0 {
		fmt.Fprintf(&b, "line %d, column %d: ", e.Position.Line, e.Position.Col)
	}
	b.WriteString(e.Message)
	if len(e.Path) > 0 {
		fmt.Fprintf(&b, " (key %s)", Key(e.Path).String())
	}
	return b.String()
}

// ParseDocument parses data as TOML and returns an editable Document. If the
// input does not parse, the [ParseError] is returned and no editing can take
// place.
func ParseDocument(data []byte) (*Document, error) {
	bom := 0
	if bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		bom = 3
	}
	doc := &docCollector{
		scalars: make(map[string]Position),
		arrays:  make(map[string][]Position),
		arrIdx:  make(map[string]int),
	}
	p, err := parseCollect(string(data[bom:]), 0, doc)
	if err != nil {
		return nil, err
	}
	return &Document{
		data:    data,
		bom:     bom,
		mapping: p.mapping,
		scalars: doc.scalars,
		arrays:  doc.arrays,
	}, nil
}

// Set queues an edit replacing the scalar at path with value, which may have
// a different type than the current value. value must be a Go type that
// encodes as a TOML scalar: string, bool, any sized int or uint that fits in
// an int64, float32, float64, or time.Time.
//
// path is a slice of literal key segments; quoted keys containing dots are
// single segments. Use a decimal segment to select an occurrence of an
// array-of-tables (e.g. "servers", "1", "port") or an element of a plain
// array (e.g. "ports", "0").
//
// Unknown paths and invalid values are reported immediately as [*EditError];
// conflicting edits are reported by [Document.Bytes] and [Document.WriteTo]
// before anything is written.
func (d *Document) Set(path []string, value any) error {
	enc, err := encodeDocValue(value)
	if err != nil {
		return d.fail(&EditError{Message: err.Error(), Path: path})
	}
	rng, err := d.find(path)
	if err != nil {
		return d.fail(err)
	}
	d.edits = append(d.edits, docEdit{
		path: append([]string(nil), path...),
		rng:  rng,
		enc:  enc,
	})
	return nil
}

// fail records the first failed edit; the entire batch is rejected when
// writing out.
func (d *Document) fail(err error) error {
	if d.err == nil {
		d.err = err
	}
	return err
}

// Bytes validates all queued edits as a batch and returns the edited
// document. If any two edits target the same token or overlapping ranges,
// or if the result would no longer parse, an error is returned and no
// output is produced.
func (d *Document) Bytes() ([]byte, error) {
	if d.err != nil {
		return nil, d.err
	}
	edits, err := d.sortedEdits()
	if err != nil {
		return nil, err
	}

	src := d.data[d.bom:]
	out := make([]byte, 0, len(src)+d.bom+64)
	out = append(out, d.data[:d.bom]...)
	last := 0
	for _, e := range edits {
		out = append(out, src[last:e.rng.Start]...)
		out = append(out, e.enc...)
		last = e.rng.Start + e.rng.Len
	}
	out = append(out, src[last:]...)

	// The decoder must always be able to read back an edited document.
	if _, err := parse(string(out[d.bom:]), 0); err != nil {
		return nil, &EditError{Message: fmt.Sprintf("edited document no longer parses: %s", err)}
	}
	return out, nil
}

// WriteTo validates all queued edits as a batch and writes the edited
// document to w. Nothing is written if any edit is invalid.
func (d *Document) WriteTo(w io.Writer) (int64, error) {
	b, err := d.Bytes()
	if err != nil {
		return 0, err
	}
	n, err := w.Write(b)
	return int64(n), err
}

// Decode decodes the edited document into v, like [Decode] on the output of
// [Document.Bytes].
func (d *Document) Decode(v any) (MetaData, error) {
	b, err := d.Bytes()
	if err != nil {
		return MetaData{}, err
	}
	return Decode(string(b), v)
}

// sortedEdits returns the queued edits sorted by token position, rejecting
// duplicate edits of the same token and overlapping ranges.
func (d *Document) sortedEdits() ([]docEdit, error) {
	edits := make([]docEdit, len(d.edits))
	copy(edits, d.edits)
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].rng.Start < edits[j].rng.Start })

	for i := 1; i < len(edits); i++ {
		prev, cur := edits[i-1], edits[i]
		if prev.rng.Start == cur.rng.Start && prev.rng.Len == cur.rng.Len {
			return nil, &EditError{
				Message:  fmt.Sprintf("conflicting edits of the same value as key %s", Key(prev.path).String()),
				Path:     cur.path,
				Position: d.posAt(cur.rng.Start),
			}
		}
		if prev.rng.Start+prev.rng.Len > cur.rng.Start {
			return nil, &EditError{
				Message:  fmt.Sprintf("edit range overlaps with an edit of key %s", Key(prev.path).String()),
				Path:     cur.path,
				Position: d.posAt(cur.rng.Start),
			}
		}
	}
	return edits, nil
}

// find resolves path to the token range of the scalar it addresses.
func (d *Document) find(path []string) (Position, error) {
	if len(path) == 0 {
		return Position{}, &EditError{Message: "empty key path"}
	}

	var (
		node any = d.mapping
		key  docKeyBuilder
	)
	for i, seg := range path {
		switch n := node.(type) {
		case map[string]any:
			v, ok := n[seg]
			if !ok {
				return Position{}, &EditError{Message: "unknown key", Path: path}
			}
			key.key(seg)
			node = v
		case []map[string]any:
			idx, err := docIndex(seg, len(n))
			if err != nil {
				return Position{}, &EditError{Message: err.Error(), Path: path}
			}
			key.index(idx)
			node = n[idx]
		case []any:
			if i != len(path)-1 {
				return Position{}, &EditError{
					Message: "cannot edit inside an array element that is a table or array",
					Path:    path,
				}
			}
			idx, err := docIndex(seg, len(n))
			if err != nil {
				return Position{}, &EditError{Message: err.Error(), Path: path}
			}
			switch n[idx].(type) {
			case map[string]any, []map[string]any, []any:
				return Position{}, &EditError{
					Message:  "can only replace scalars, not tables or arrays",
					Path:     path,
					Position: d.posAt(d.arrays[key.String()][idx].Start),
				}
			}
			return d.arrays[key.String()][idx], nil
		default:
			return Position{}, &EditError{Message: "unknown key", Path: path}
		}
	}

	switch node.(type) {
	case map[string]any, []map[string]any, []any:
		return Position{}, &EditError{
			Message: "can only replace scalars, not tables or arrays",
			Path:    path,
		}
	}
	rng, ok := d.scalars[key.String()]
	if !ok {
		return Position{}, &EditError{Message: "unknown key", Path: path}
	}
	return rng, nil
}

func docIndex(seg string, length int) (int, error) {
	idx, err := strconv.Atoi(seg)
	if err != nil {
		return 0, fmt.Errorf("%q is not an array index", seg)
	}
	if idx < 0 || idx >= length {
		return 0, fmt.Errorf("array index %d out of range (length %d)", idx, length)
	}
	return idx, nil
}

// posAt converts a byte offset in the document to a line/column Position.
func (d *Document) posAt(offset int) Position {
	src := string(d.data[d.bom:])
	line := 1 + strings.Count(src[:offset], "\n")
	return Position{Line: line, Start: offset}.withCol(src)
}

// encodeDocValue encodes a Go value as a TOML scalar, matching the syntax
// [Encoder] produces.
func encodeDocValue(v any) (string, error) {
	switch v := v.(type) {
	case string:
		return `"` + dblQuotedReplacer.Replace(v) + `"`, nil
	case bool:
		return strconv.FormatBool(v), nil
	case int:
		return strconv.FormatInt(int64(v), 10), nil
	case int8:
		return strconv.FormatInt(int64(v), 10), nil
	case int16:
		return strconv.FormatInt(int64(v), 10), nil
	case int32:
		return strconv.FormatInt(int64(v), 10), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case uint:
		return encodeDocUint(uint64(v))
	case uint8:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(v), 10), nil
	case uint64:
		return encodeDocUint(v)
	case float32:
		return docFloat(float64(v), 32), nil
	case float64:
		return docFloat(v, 64), nil
	case time.Time:
		format := time.RFC3339Nano
		switch v.Location() {
		case internal.LocalDatetime:
			format = "2006-01-02T15:04:05.999999999"
		case internal.LocalDate:
			format = "2006-01-02"
		case internal.LocalTime:
			format = "15:04:05.999999999"
		}
		return v.Format(format), nil
	default:
		return "", fmt.Errorf("cannot encode Go type %T as a TOML scalar", v)
	}
}

func encodeDocUint(v uint64) (string, error) {
	if v > math.MaxInt64 {
		return "", fmt.Errorf("uint64 value %d does not fit in a TOML integer", v)
	}
	return strconv.FormatUint(v, 10), nil
}

func docFloat(f float64, bitSize int) string {
	switch {
	case math.IsNaN(f):
		if math.Signbit(f) {
			return "-nan"
		}
		return "nan"
	case math.IsInf(f, 0):
		if math.Signbit(f) {
			return "-inf"
		}
		return "inf"
	}
	return floatAddDecimal(strconv.FormatFloat(f, 'g', -1, bitSize))
}

// docCollector records the token ranges of every scalar value while parsing,
// so a Document can replace them in place.
type docCollector struct {
	scalars map[string]Position   // Scalar key/value pairs: doc key → token range.
	arrays  map[string][]Position // Plain array elements: doc key → token ranges.
	arrIdx  map[string]int        // Array-of-tables key → current occurrence index.
}

// docKeyBuilder encodes a key path unambiguously: 'k' segments are literal
// keys, 'i' segments are array-of-tables occurrence indices.
type docKeyBuilder struct{ b strings.Builder }

func (d *docKeyBuilder) key(s string)   { d.seg('k', s) }
func (d *docKeyBuilder) index(i int)    { d.seg('i', strconv.Itoa(i)) }
func (d *docKeyBuilder) String() string { return d.b.String() }

func (d *docKeyBuilder) seg(kind byte, s string) {
	d.b.WriteByte(kind)
	d.b.WriteString(strconv.Itoa(len(s)))
	d.b.WriteByte(':')
	d.b.WriteString(s)
}

// docKey builds the doc key for the current context plus leaf, inserting the
// occurrence index after every array-of-tables segment.
func (p *parser) docKey(leaf string) string {
	var key docKeyBuilder
	for i := range p.context {
		key.key(p.context[i])
		if p.keyInfo[Key(p.context[:i+1]).String()].tomlType == tomlArrayHash {
			key.index(p.doc.arrIdx[Key(p.context[:i+1]).String()])
		}
	}
	key.key(leaf)
	return key.String()
}

// docTokenRange returns the byte range of a value token, expanding string
// tokens (whose lexer position excludes the quotes) to include the
// surrounding quotes.
func docTokenRange(input string, it item) Position {
	start, length := it.pos.Start, it.pos.Len
	switch it.typ {
	case itemString, itemStringEsc, itemMultilineString, itemRawString, itemRawMultilineString:
		q := input[start-1]
		n := 1
		if start >= 3 && input[start-2] == q && input[start-3] == q {
			n = 3
		}
		start -= n
		length += 2 * n
	}
	return Position{Start: start, Len: length}
}
