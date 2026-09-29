package toml

import (
	"fmt"
	"io"
	"reflect"
	"strings"
)

// DecodeAtomic decodes the TOML data into the pointer v atomically: the data
// is first decoded into an isolated deep copy of v's current value, and only
// if the entire decode succeeds is v replaced with the decoded copy. If
// decoding fails for any reason, v is left completely untouched, including any
// maps, slices, or pointers it (indirectly) refers to.
//
// Values in v that are not set by the TOML data are preserved, exactly as with
// [Decode].
//
// DecodeAtomic supports data composed of exported struct fields, pointers,
// maps, slices, arrays, basic types, and the TOML-native [time.Time] type.
// Types implementing [Unmarshaler] (UnmarshalTOML) or
// [encoding.TextUnmarshaler] are not supported and are rejected with an error
// before anything is decoded. Cyclic values (a value that directly or
// indirectly refers to itself) are likewise rejected rather than recursing
// forever.
//
// The returned MetaData is identical to what [Decode] would have returned.
func DecodeAtomic(data string, v any) (MetaData, error) {
	return NewDecoder(strings.NewReader(data)).DecodeAtomic(v)
}

// DecodeAtomic decodes the TOML data from the Decoder's reader into the
// pointer v atomically. See the package-level [DecodeAtomic] function for a
// full description of the semantics and limitations.
func (dec *Decoder) DecodeAtomic(v any) (MetaData, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer {
		s := "%q"
		if reflect.TypeOf(v) == nil {
			s = "%v"
		}

		return MetaData{}, fmt.Errorf("toml: cannot decode to non-pointer "+s, reflect.TypeOf(v))
	}
	if rv.IsNil() {
		return MetaData{}, fmt.Errorf("toml: cannot decode to nil value of %q", reflect.TypeOf(v))
	}

	// Resolve pointers, allocating nil ones, without indirect()'s special
	// case for custom unmarshalers: those are rejected below instead.
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			rv.Set(reflect.New(rv.Type().Elem()))
		}
		rv = rv.Elem()
	}
	rt := rv.Type()
	if rv.Kind() != reflect.Struct && rv.Kind() != reflect.Map &&
		!(rv.Kind() == reflect.Interface && rv.NumMethod() == 0) {
		return MetaData{}, fmt.Errorf("toml: cannot decode to type %s", rt)
	}

	// Reject custom unmarshalers anywhere in the type graph before decoding
	// anything.
	if err := atomicCheckType(rt, make(map[reflect.Type]bool)); err != nil {
		return MetaData{}, err
	}

	// Deep-copy the current value so that decoding cannot mutate the
	// caller's value through shared pointers, maps, or slices. This also
	// rejects cyclic values.
	cp, err := atomicCopy(rv, make(map[atomicVisit]bool))
	if err != nil {
		return MetaData{}, err
	}
	work := reflect.New(rt).Elem()
	work.Set(cp)

	data, err := io.ReadAll(dec.r)
	if err != nil {
		return MetaData{}, err
	}

	p, err := parse(string(data), dec.maxDepth)
	if err != nil {
		return MetaData{}, err
	}

	md := MetaData{
		mapping: p.mapping,
		keyInfo: p.keyInfo,
		keys:    p.ordered,
		decoded: make(map[string]struct{}, len(p.ordered)),
		context: nil,
		data:    data,
	}
	if err := md.unify(p.mapping, work); err != nil {
		return MetaData{}, err
	}

	// Everything decoded successfully: replace the target.
	rv.Set(work)
	return md, nil
}

// atomicCheckType returns an error if any type reachable from t implements
// UnmarshalTOML or encoding.TextUnmarshaler, which DecodeAtomic does not
// support. The TOML-native time.Time type is exempt, as the decoder handles
// it natively.
func atomicCheckType(t reflect.Type, seen map[reflect.Type]bool) error {
	if seen[t] {
		return nil
	}
	seen[t] = true

	if t == timeType {
		return nil
	}
	if t.Implements(unmarshalToml) || t.Implements(unmarshalText) ||
		reflect.PointerTo(t).Implements(unmarshalToml) || reflect.PointerTo(t).Implements(unmarshalText) {
		return fmt.Errorf("toml: DecodeAtomic does not support %s, which implements UnmarshalTOML or encoding.TextUnmarshaler", t)
	}

	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return atomicCheckType(t.Elem(), seen)
	case reflect.Map:
		if err := atomicCheckType(t.Key(), seen); err != nil {
			return err
		}
		return atomicCheckType(t.Elem(), seen)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() && !f.Anonymous {
				continue
			}
			if err := atomicCheckType(f.Type, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

// atomicVisit identifies a value currently being copied, used to detect
// cycles in the value graph.
type atomicVisit struct {
	typ reflect.Type
	ptr uintptr
}

// atomicCopy returns a deep copy of rv. Pointers, maps, slices, arrays,
// structs, and interfaces are copied recursively; anything else is copied by
// value. Cyclic values are rejected with an error.
func atomicCopy(rv reflect.Value, visiting map[atomicVisit]bool) (reflect.Value, error) {
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return reflect.Zero(rv.Type()), nil
		}
		leave, err := atomicEnter(rv, visiting)
		if err != nil {
			return reflect.Value{}, err
		}
		defer leave()

		elem, err := atomicCopy(rv.Elem(), visiting)
		if err != nil {
			return reflect.Value{}, err
		}
		cp := reflect.New(rv.Type().Elem())
		cp.Elem().Set(elem)
		return cp, nil

	case reflect.Map:
		if rv.IsNil() {
			return reflect.Zero(rv.Type()), nil
		}
		leave, err := atomicEnter(rv, visiting)
		if err != nil {
			return reflect.Value{}, err
		}
		defer leave()

		cp := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			k, err := atomicCopy(iter.Key(), visiting)
			if err != nil {
				return reflect.Value{}, err
			}
			v, err := atomicCopy(iter.Value(), visiting)
			if err != nil {
				return reflect.Value{}, err
			}
			cp.SetMapIndex(k, v)
		}
		return cp, nil

	case reflect.Slice:
		if rv.IsNil() {
			return reflect.Zero(rv.Type()), nil
		}
		if rv.Len() == 0 {
			return reflect.MakeSlice(rv.Type(), 0, 0), nil
		}
		leave, err := atomicEnter(rv, visiting)
		if err != nil {
			return reflect.Value{}, err
		}
		defer leave()

		cp := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			elem, err := atomicCopy(rv.Index(i), visiting)
			if err != nil {
				return reflect.Value{}, err
			}
			cp.Index(i).Set(elem)
		}
		return cp, nil

	case reflect.Array:
		cp := reflect.New(rv.Type()).Elem()
		for i := 0; i < rv.Len(); i++ {
			elem, err := atomicCopy(rv.Index(i), visiting)
			if err != nil {
				return reflect.Value{}, err
			}
			cp.Index(i).Set(elem)
		}
		return cp, nil

	case reflect.Struct:
		// Copy the whole struct first so unexported fields (e.g. in
		// time.Time) come along, then deep-copy the exported fields.
		cp := reflect.New(rv.Type()).Elem()
		cp.Set(rv)
		for i := 0; i < rv.NumField(); i++ {
			if !rv.Type().Field(i).IsExported() || !cp.Field(i).CanSet() {
				continue
			}
			elem, err := atomicCopy(rv.Field(i), visiting)
			if err != nil {
				return reflect.Value{}, err
			}
			cp.Field(i).Set(elem)
		}
		return cp, nil

	case reflect.Interface:
		if rv.IsNil() {
			return reflect.Zero(rv.Type()), nil
		}
		elem, err := atomicCopy(rv.Elem(), visiting)
		if err != nil {
			return reflect.Value{}, err
		}
		cp := reflect.New(rv.Type()).Elem()
		cp.Set(elem)
		return cp, nil

	default:
		return rv, nil
	}
}

// atomicEnter marks rv as being copied and returns a function to unmark it.
// It returns an error if rv is already being copied, meaning the value graph
// contains a cycle.
func atomicEnter(rv reflect.Value, visiting map[atomicVisit]bool) (func(), error) {
	key := atomicVisit{rv.Type(), uintptr(rv.UnsafePointer())}
	if visiting[key] {
		return nil, fmt.Errorf("toml: DecodeAtomic does not support cyclic values: found a cycle through %s", rv.Type())
	}
	visiting[key] = true
	return func() { delete(visiting, key) }, nil
}
