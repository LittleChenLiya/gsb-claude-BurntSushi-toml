package toml

import (
	"bytes"
	"encoding"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
)

// CircularError is returned by [SafeEncoder.Encode] and [MarshalSafe] when a
// value refers back to itself along the current key path, which would make
// the encoder recurse forever.
//
// The same object appearing in several unrelated branches of the input is
// not a circular reference and is encoded normally; only a reference that
// loops back to an object already on the current path is reported.
type CircularError struct {
	// Path is the key path (using TOML key names and [i] indexes) at which
	// the circular reference was found, for example:
	//
	//	Parent.Children[2].Parent
	Path string
}

func (e *CircularError) Error() string {
	return fmt.Sprintf("toml: circular reference at key %q", e.Path)
}

// MarshalSafe is like [Marshal], but checks the value for circular
// references before encoding, like [SafeEncoder.Encode] does.
func MarshalSafe(v any) ([]byte, error) {
	buff := new(bytes.Buffer)
	if err := NewSafeEncoder(buff).Encode(v); err != nil {
		return nil, err
	}
	return buff.Bytes(), nil
}

// SafeEncoder encodes a Go value to a TOML document like [Encoder], but
// first walks the value to detect circular references and to prepare the
// output of any [Marshaler] and [encoding.TextMarshaler] values.
//
// The walk follows the same rules as the encoder: it uses the toml struct
// tags, skips fields that are never encoded (toml:"-", unexported fields,
// empty omitempty or zero omitzero fields, nil values), and treats values
// implementing Marshaler or encoding.TextMarshaler as leaves. A circular
// reference reported by the walk therefore always points at data that would
// really be encoded, and cycles hidden in skipped fields are ignored.
//
// Custom marshalers are called only once for every distinct (comparable)
// value and the result is reused if the value is encoded more than once. If
// a marshaler returns an error, or returns more than MaxMarshalerBytes
// bytes, encoding stops and the error is returned.
//
// Nothing is written to the writer until the entire document has been
// prepared successfully, so a failed Encode leaves the writer untouched.
// Errors from the writer itself are returned as-is.
type SafeEncoder struct {
	Indent string // string for a single indentation level; default is two spaces.

	// MaxMarshalerBytes is the maximum number of bytes accepted from a
	// single MarshalTOML or MarshalText call; 0 means no limit.
	MaxMarshalerBytes int

	w io.Writer
}

// NewSafeEncoder creates a new [SafeEncoder] writing to w.
func NewSafeEncoder(w io.Writer) *SafeEncoder {
	return &SafeEncoder{w: w, Indent: "  "}
}

// Encode writes a TOML representation of the Go value to the [SafeEncoder]'s
// writer.
//
// A [*CircularError] is returned if the value refers back to itself along a
// key path. Any other error from preparing the document (including custom
// marshaler errors) is returned before anything is written to the writer.
func (enc *SafeEncoder) Encode(v any) error {
	c := &safeChecker{
		ancestors:         make(map[visit]struct{}),
		marshaled:         make(map[any][]byte),
		maxMarshalerBytes: enc.MaxMarshalerBytes,
	}
	if err := c.check(reflect.ValueOf(v), ""); err != nil {
		return err
	}

	// Prepare the full document in a buffer first; only write to the real
	// writer once everything succeeded.
	var buf bytes.Buffer
	inner := NewEncoder(&buf)
	inner.Indent = enc.Indent
	inner.marshalCache = c.marshaled
	if err := inner.Encode(v); err != nil {
		return err
	}
	_, err := enc.w.Write(buf.Bytes())
	return err
}

// visit identifies a value by its type and address while walking the value
// graph; only maps, slices, and pointers have an identity that can loop.
type visit struct {
	typ reflect.Type
	ptr uintptr
}

// safeChecker walks a Go value ahead of encoding to detect circular
// references and to call custom marshalers exactly once.
type safeChecker struct {
	// ancestors holds the maps, slices, and pointers on the current path;
	// entries are removed again when the walk leaves the value, so the same
	// object used from different branches is not reported as a cycle.
	ancestors map[visit]struct{}

	// marshaled caches the output of every custom marshaler, keyed by the
	// (comparable) value, so each one is called only once.
	marshaled map[any][]byte

	maxMarshalerBytes int
}

// check walks rv, which sits at path, returning a [*CircularError] if rv
// refers back to a value already on the path.
func (c *safeChecker) check(rv reflect.Value, path string) error {
	rv, entered, err := c.indirect(rv, path)
	if err != nil {
		return err
	}
	defer c.pop(entered)

	if !rv.IsValid() || isNil(rv) {
		return nil
	}
	if rv.Type() == timeType {
		return nil
	}
	if rv.Type() == primitiveType {
		return c.check(reflect.ValueOf(rv.Interface().(Primitive).undecoded), path)
	}
	if isMarshaler(rv) {
		return c.marshal(rv, path)
	}
	if rv.CanAddr() {
		if pv := rv.Addr(); isMarshaler(pv) {
			return c.marshal(pv, path)
		}
	}

	switch rv.Kind() {
	case reflect.Struct:
		return c.checkStruct(rv, path)
	case reflect.Map:
		return c.checkMap(rv, path)
	case reflect.Slice, reflect.Array:
		return c.checkSlice(rv, path)
	}
	return nil
}

// indirect unwraps interfaces and pointers like eindirect, but enters every
// pointer on the way in the ancestor set and reports a [*CircularError] if a
// pointer already on the current path is found. The returned visits must be
// popped once the walk is done with the value.
func (c *safeChecker) indirect(rv reflect.Value, path string) (reflect.Value, []visit, error) {
	var entered []visit
	for {
		switch rv.Kind() {
		case reflect.Interface:
			if rv.IsNil() {
				return rv, entered, nil
			}
			rv = rv.Elem()
		case reflect.Pointer:
			if rv.IsNil() {
				return rv, entered, nil
			}
			v := visit{rv.Type(), rv.Pointer()}
			if _, ok := c.ancestors[v]; ok {
				return rv, entered, &CircularError{Path: path}
			}
			c.ancestors[v] = struct{}{}
			entered = append(entered, v)
			rv = rv.Elem()
		default:
			return rv, entered, nil
		}
	}
}

func (c *safeChecker) pop(entered []visit) {
	for _, v := range entered {
		delete(c.ancestors, v)
	}
}

// marshal calls the custom marshaler for rv once and caches the result for
// the encoder to reuse.
func (c *safeChecker) marshal(rv reflect.Value, path string) error {
	var key any
	cacheable := rv.Type().Comparable()
	if cacheable {
		key = rv.Interface()
		if _, ok := c.marshaled[key]; ok {
			return nil // Already called; the cached result will be reused.
		}
	}

	var (
		b   []byte
		err error
	)
	if m, ok := rv.Interface().(Marshaler); ok {
		b, err = m.MarshalTOML()
		if err == nil && b == nil {
			err = errors.New("MarshalTOML returned nil and no error")
		}
	} else {
		b, err = rv.Interface().(encoding.TextMarshaler).MarshalText()
		if err == nil && b == nil {
			err = errors.New("MarshalText returned nil and no error")
		}
	}
	if err != nil {
		return err
	}
	if c.maxMarshalerBytes > 0 && len(b) > c.maxMarshalerBytes {
		return fmt.Errorf("toml: custom marshaler at key %q returned %d bytes, exceeding the limit of %d bytes",
			path, len(b), c.maxMarshalerBytes)
	}
	if cacheable {
		c.marshaled[key] = b
	}
	return nil
}

// checkStruct walks the fields the encoder would write, following the toml
// tags, embedded structs, and field visibility rules.
func (c *safeChecker) checkStruct(rv reflect.Value, path string) error {
	var walk func(rt reflect.Type, rv reflect.Value) error
	walk = func(rt reflect.Type, rv reflect.Value) error {
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			isEmbed := f.Anonymous && pointerTo(f.Type).Kind() == reflect.Struct
			if f.PkgPath != "" && !isEmbed { // Skip unexported fields.
				continue
			}
			opts := getOptions(f.Tag)
			if opts.skip {
				continue
			}
			frv := rv.Field(i)

			// Anonymous struct fields without a tag name are flattened,
			// like the encoder does.
			if isEmbed && opts.name == "" {
				efrv, entered, err := c.indirect(frv, path)
				if err != nil {
					return err
				}
				if efrv.Kind() == reflect.Struct {
					err := walk(efrv.Type(), efrv)
					c.pop(entered)
					if err != nil {
						return err
					}
					continue
				}
				c.pop(entered)
			}

			// Skip fields the encoder will not write anything for.
			if opts.omitempty && isEmpty(frv) {
				continue
			}
			efrv := eindirect(frv)
			if isNil(efrv) {
				continue
			}
			if opts.omitzero && isZero(efrv) {
				continue
			}

			keyName := f.Name
			if opts.name != "" {
				keyName = opts.name
			}
			if err := c.check(frv, joinPath(path, keyName)); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(rv.Type(), rv)
}

func (c *safeChecker) checkMap(rv reflect.Value, path string) error {
	if rv.Type().Key().Kind() != reflect.String {
		// The encoder refuses these maps without looking at the values,
		// so there is nothing to check here.
		return nil
	}
	v := visit{rv.Type(), rv.Pointer()}
	if _, ok := c.ancestors[v]; ok {
		return &CircularError{Path: path}
	}
	c.ancestors[v] = struct{}{}
	defer c.pop([]visit{v})

	for _, k := range rv.MapKeys() {
		if err := c.check(rv.MapIndex(k), joinPath(path, k.String())); err != nil {
			return err
		}
	}
	return nil
}

func (c *safeChecker) checkSlice(rv reflect.Value, path string) error {
	if rv.Kind() == reflect.Slice && rv.Len() > 0 {
		v := visit{rv.Type(), rv.Pointer()}
		if _, ok := c.ancestors[v]; ok {
			return &CircularError{Path: path}
		}
		c.ancestors[v] = struct{}{}
		defer c.pop([]visit{v})
	}
	for i := 0; i < rv.Len(); i++ {
		if err := c.check(rv.Index(i), joinPath(path, "["+strconv.Itoa(i)+"]")); err != nil {
			return err
		}
	}
	return nil
}

// joinPath appends a key name or [i] index to a key path.
func joinPath(path, elem string) string {
	if path == "" {
		return elem
	}
	if strings.HasPrefix(elem, "[") {
		return path + elem
	}
	return path + "." + elem
}
