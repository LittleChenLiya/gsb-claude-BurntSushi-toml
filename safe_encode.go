package toml

import (
	"bytes"
	"encoding"
	"fmt"
	"io"
	"reflect"
	"unsafe"
)

// SafeEncoder encodes a Go value to a TOML document, like [Encoder], but adds
// a safety pass before anything is written to the underlying writer:
//
//   - The value is checked for cyclic references first. A cycle is reported
//     only when encoding would actually recurse forever: the same map, slice,
//     or pointer (structs are always reached through one of those) occurring
//     as its own descendant along the current path. The same object
//     referenced from multiple branches is encoded normally.
//   - The check follows the same rules as the encoder itself: fields that
//     would not be encoded (unexported fields, fields tagged with "-",
//     empty "omitempty" or zero "omitzero" fields, nil values, and values
//     handled through [Marshaler] or [encoding.TextMarshaler]) are not
//     inspected.
//   - Values implementing [Marshaler] or [encoding.TextMarshaler] are treated
//     as leaves; every distinct value is marshalled exactly once and the
//     result is reused.
//   - The entire document is prepared in memory first; nothing is written to
//     the underlying writer until the document is complete. Errors from the
//     writer itself are returned as-is.
//
// The returned error for a cyclic value contains the path of fields and/or
// indexes at which the cycle was detected, for example:
//
//	toml: cyclic reference detected at key 'a.b[2].c'
type SafeEncoder struct {
	Indent   string // string for a single indentation level; default is two spaces.
	MaxBytes int    // maximum size of the encoded document in bytes; 0 means no limit.

	w io.Writer
}

// NewSafeEncoder creates a new SafeEncoder.
func NewSafeEncoder(w io.Writer) *SafeEncoder {
	return &SafeEncoder{w: w, Indent: "  "}
}

// Encode writes a TOML representation of the Go value to the
// [SafeEncoder]'s writer.
//
// An error is returned – before anything is written – if the value contains
// a cyclic reference, cannot be encoded to a valid TOML document, or the
// encoded document exceeds MaxBytes.
func (enc *SafeEncoder) Encode(v any) error {
	if err := checkCycles(reflect.ValueOf(v)); err != nil {
		return err
	}

	var buf bytes.Buffer
	inner := NewEncoder(&limitWriter{w: &buf, max: enc.MaxBytes})
	inner.Indent = enc.Indent
	inner.marshalCache = make(map[marshalKey]marshalResult)
	if err := inner.Encode(v); err != nil {
		return err
	}

	_, err := enc.w.Write(buf.Bytes())
	return err
}

// limitWriter fails once more than max bytes have been written.
type limitWriter struct {
	w   io.Writer
	max int
	n   int
}

func (lw *limitWriter) Write(p []byte) (int, error) {
	if lw.max > 0 && lw.n+len(p) > lw.max {
		return 0, fmt.Errorf("toml: encoded output exceeds the maximum of %d bytes", lw.max)
	}
	n, err := lw.w.Write(p)
	lw.n += n
	return n, err
}

// marshalKey identifies a value implementing Marshaler or
// encoding.TextMarshaler by type and address.
type marshalKey struct {
	typ reflect.Type
	ptr unsafe.Pointer
}

type marshalResult struct {
	b   []byte
	err error
}

func marshalCacheKey(rv reflect.Value) (marshalKey, bool) {
	switch {
	case rv.Kind() == reflect.Pointer:
		return marshalKey{rv.Type(), rv.UnsafePointer()}, true
	case rv.CanAddr():
		return marshalKey{reflect.PtrTo(rv.Type()), unsafe.Pointer(rv.UnsafeAddr())}, true
	}
	return marshalKey{}, false
}

func (enc *Encoder) marshalTOML(rv reflect.Value, m Marshaler) ([]byte, error) {
	if enc.marshalCache == nil {
		return m.MarshalTOML()
	}
	if key, ok := marshalCacheKey(rv); ok {
		if r, ok := enc.marshalCache[key]; ok {
			return r.b, r.err
		}
		b, err := m.MarshalTOML()
		enc.marshalCache[key] = marshalResult{b, err}
		return b, err
	}
	return m.MarshalTOML()
}

func (enc *Encoder) marshalText(rv reflect.Value, m encoding.TextMarshaler) ([]byte, error) {
	if enc.marshalCache == nil {
		return m.MarshalText()
	}
	if key, ok := marshalCacheKey(rv); ok {
		if r, ok := enc.marshalCache[key]; ok {
			return r.b, r.err
		}
		b, err := m.MarshalText()
		enc.marshalCache[key] = marshalResult{b, err}
		return b, err
	}
	return m.MarshalText()
}

// visit identifies a map, slice, or pointer on the current path. For slices
// the length and capacity are included so that a sub-slice sharing only the
// backing array of an ancestor is not mistaken for a cycle.
type visit struct {
	typ      reflect.Type
	ptr      unsafe.Pointer
	len, cap int
}

// cycleChecker walks a value the same way the encoder would and reports any
// map, slice, or pointer that occurs as its own descendant.
type cycleChecker struct {
	onPath map[visit]bool
	segs   []string
}

func checkCycles(rv reflect.Value) error {
	if !rv.IsValid() {
		return nil
	}
	c := &cycleChecker{onPath: make(map[visit]bool)}
	return c.value(rv)
}

func (c *cycleChecker) cycleError() error {
	var path string
	for i, seg := range c.segs {
		if i > 0 && len(seg) > 0 && seg[0] != '[' {
			path += "."
		}
		path += seg
	}
	return fmt.Errorf("toml: cyclic reference detected at key '%s'", path)
}

func (c *cycleChecker) enter(v visit) error {
	if c.onPath[v] {
		return c.cycleError()
	}
	c.onPath[v] = true
	return nil
}

func (c *cycleChecker) leave(v visit) { delete(c.onPath, v) }

func (c *cycleChecker) value(rv reflect.Value) error {
	if !rv.IsValid() {
		return nil
	}

	// Resolve interfaces and pointers, mirroring eindirect but tracking
	// pointer identity so that pointer-only cycles are detected.
	for {
		switch rv.Kind() {
		case reflect.Interface:
			if rv.IsNil() {
				return nil
			}
			rv = rv.Elem()
			continue
		case reflect.Pointer:
			if rv.IsNil() {
				return nil
			}
			if isMarshaler(rv) {
				return nil
			}
			v := visit{typ: rv.Type(), ptr: rv.UnsafePointer()}
			if err := c.enter(v); err != nil {
				return err
			}
			err := c.value(rv.Elem())
			c.leave(v)
			return err
		}
		break
	}

	// Values encoded through a custom marshaler are leaves; their output is
	// opaque and cannot contain a reference back into the Go value.
	if isMarshaler(rv) {
		return nil
	}
	if rv.CanAddr() {
		if pv := rv.Addr(); isMarshaler(pv) {
			return nil
		}
	}

	if rv.Type() == primitiveType {
		return c.value(reflect.ValueOf(rv.Interface().(Primitive).undecoded))
	}

	switch rv.Kind() {
	case reflect.Map:
		if rv.IsNil() {
			return nil
		}
		if rv.Type().Key().Kind() != reflect.String {
			// The encoder reports errNonString for this; no need to walk it.
			return nil
		}
		v := visit{typ: rv.Type(), ptr: rv.UnsafePointer()}
		if err := c.enter(v); err != nil {
			return err
		}
		defer c.leave(v)
		for _, k := range rv.MapKeys() {
			c.segs = append(c.segs, k.String())
			err := c.value(rv.MapIndex(k))
			c.segs = c.segs[:len(c.segs)-1]
			if err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice:
		if rv.IsNil() || rv.Len() == 0 {
			return nil
		}
		v := visit{typ: rv.Type(), ptr: rv.UnsafePointer(), len: rv.Len(), cap: rv.Cap()}
		if err := c.enter(v); err != nil {
			return err
		}
		defer c.leave(v)
		return c.elements(rv)
	case reflect.Array:
		return c.elements(rv)
	case reflect.Struct:
		return c.structFields(rv)
	default:
		// Scalars, and types the encoder itself will reject.
		return nil
	}
}

func (c *cycleChecker) elements(rv reflect.Value) error {
	for i := 0; i < rv.Len(); i++ {
		c.segs = append(c.segs, fmt.Sprintf("[%d]", i))
		err := c.value(rv.Index(i))
		c.segs = c.segs[:len(c.segs)-1]
		if err != nil {
			return err
		}
	}
	return nil
}

// structFields mirrors the field selection of the encoder's eStruct: only
// fields that would actually be encoded are checked.
func (c *cycleChecker) structFields(rv reflect.Value) error {
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		isEmbed := f.Anonymous && pointerTo(f.Type).Kind() == reflect.Struct
		if f.PkgPath != "" && !isEmbed { /// Skip unexported fields.
			continue
		}
		opts := getOptions(f.Tag)
		if opts.skip {
			continue
		}

		frv := eindirect(rv.Field(i))

		// Anonymous struct fields without a tag name are encoded as if their
		// fields belong to the parent struct.
		if isEmbed && opts.name == "" && frv.Kind() == reflect.Struct {
			// Route through value() so that an embedded pointer is tracked:
			// a cycle through only embedded pointers is still a cycle.
			if err := c.value(rv.Field(i)); err != nil {
				return err
			}
			continue
		}

		fieldVal := rv.Field(i)
		if opts.omitempty && isEmpty(fieldVal) {
			continue
		}
		fieldVal = eindirect(fieldVal)
		if isNil(fieldVal) { /// Nothing is written for nil fields.
			continue
		}
		if opts.omitzero && isZero(fieldVal) {
			continue
		}

		keyName := f.Name
		if opts.name != "" {
			keyName = opts.name
		}

		c.segs = append(c.segs, keyName)
		err := c.value(rv.Field(i))
		c.segs = c.segs[:len(c.segs)-1]
		if err != nil {
			return err
		}
	}
	return nil
}
