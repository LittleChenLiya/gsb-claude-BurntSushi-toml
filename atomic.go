package toml

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
)

// DecodeAtomic decodes the TOML data in to the pointer v atomically: if
// decoding fails for any reason, v is left completely untouched.
//
// See [Decoder.DecodeAtomic] for a description of the decoding process.
func DecodeAtomic(data string, v any) (MetaData, error) {
	return NewDecoder(strings.NewReader(data)).DecodeAtomic(v)
}

// DecodeAtomic decodes the TOML data in to the pointer v atomically: the TOML
// data is decoded into a detached deep copy of v first, and v is replaced with
// the decoded copy only when the entire decode succeeds. If decoding fails
// for any reason, v and every value reachable from it are left completely
// untouched.
//
// Values in v that are not set by the TOML data are preserved, and the
// returned MetaData reports decoded and undecoded keys exactly like
// [Decoder.Decode].
//
// Only data composed of structs (exported fields), pointers, maps, slices,
// arrays, basic types, and TOML's native time type ([time.Time]) is
// supported. Types implementing [Unmarshaler] or
// [encoding.TextUnmarshaler] are rejected before any decoding takes place, as
// are cyclic data structures.
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

	// Reject custom decoders up front: DecodeAtomic supports only data made
	// of structs (exported fields), pointers, maps, slices, arrays, basic
	// types, and TOML's native time type.
	if err := checkAtomicType(rv.Type(), make(map[reflect.Type]bool)); err != nil {
		return MetaData{}, err
	}

	// Check if this is a supported type: struct, map, or any. Unlike Decode,
	// types implementing UnmarshalTOML or UnmarshalText were rejected above.
	rt := rv.Type()
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct && rt.Kind() != reflect.Map &&
		!(rt.Kind() == reflect.Interface && rt.NumMethod() == 0) {
		return MetaData{}, fmt.Errorf("toml: cannot decode to type %s", rt)
	}

	data, err := io.ReadAll(dec.r)
	if err != nil {
		return MetaData{}, err
	}

	p, err := parse(string(data), dec.maxDepth)
	if err != nil {
		return MetaData{}, err
	}

	// Deep-copy the target into a detached clone. This also rejects cyclic
	// data structures, before any decoding takes place.
	cl := atomicCloner{
		done:    make(map[atomicVisit]reflect.Value),
		inStack: make(map[atomicVisit]bool),
	}
	copied, err := cl.clone(rv.Elem())
	if err != nil {
		return MetaData{}, err
	}
	cv := reflect.New(rv.Elem().Type())
	cv.Elem().Set(copied)

	md := MetaData{
		mapping: p.mapping,
		keyInfo: p.keyInfo,
		keys:    p.ordered,
		decoded: make(map[string]struct{}, len(p.ordered)),
		context: nil,
		data:    data,
	}
	if err := md.unify(p.mapping, indirect(cv)); err != nil {
		return md, err
	}

	// Decoding fully succeeded; replace the target with the decoded copy.
	rv.Elem().Set(cv.Elem())
	return md, nil
}

var atomicTimeType = reflect.TypeOf(time.Time{})

// checkAtomicType reports an error if any type reachable from t implements
// Unmarshaler or encoding.TextUnmarshaler, as DecodeAtomic does not support
// custom decoding. TOML's native time type (time.Time) is exempt.
func checkAtomicType(t reflect.Type, seen map[reflect.Type]bool) error {
	if seen[t] {
		return nil
	}
	seen[t] = true

	if t == atomicTimeType {
		return nil
	}
	if t.Implements(unmarshalToml) || reflect.PointerTo(t).Implements(unmarshalToml) {
		return fmt.Errorf("toml: DecodeAtomic does not support custom decoding via UnmarshalTOML (type %s)", t)
	}
	if t.Implements(unmarshalText) || reflect.PointerTo(t).Implements(unmarshalText) {
		return fmt.Errorf("toml: DecodeAtomic does not support custom decoding via encoding.TextUnmarshaler (type %s)", t)
	}

	switch t.Kind() {
	case reflect.Pointer:
		return checkAtomicType(t.Elem(), seen)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if err := checkAtomicType(t.Field(i).Type, seen); err != nil {
				return err
			}
		}
	case reflect.Map:
		if err := checkAtomicType(t.Key(), seen); err != nil {
			return err
		}
		return checkAtomicType(t.Elem(), seen)
	case reflect.Slice, reflect.Array:
		return checkAtomicType(t.Elem(), seen)
	}
	return nil
}

// atomicVisit identifies a pointer, map, or slice inside the value graph of
// the decode target.
type atomicVisit struct {
	typ reflect.Type
	ptr uintptr
}

// atomicCloner deep-copies a value graph, detaching all pointers, maps, and
// slices so that decoding into the copy can never modify the original.
// Shared references are preserved, and cyclic data structures are rejected.
type atomicCloner struct {
	done    map[atomicVisit]reflect.Value
	inStack map[atomicVisit]bool
}

func (c *atomicCloner) clone(rv reflect.Value) (reflect.Value, error) {
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return reflect.Zero(rv.Type()), nil
		}
		v, tracked := atomicVisit{}, false
		if rv.Type().Elem().Size() > 0 {
			v, tracked = atomicVisit{rv.Type(), uintptr(rv.UnsafePointer())}, true
			if c.inStack[v] {
				return reflect.Value{}, atomicCycleError(rv.Type())
			}
			if cl, ok := c.done[v]; ok {
				return cl, nil
			}
			c.inStack[v] = true
		}
		nv := reflect.New(rv.Type().Elem())
		elem, err := c.clone(rv.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		nv.Elem().Set(elem)
		if tracked {
			delete(c.inStack, v)
			c.done[v] = nv
		}
		return nv, nil

	case reflect.Map:
		if rv.IsNil() {
			return reflect.Zero(rv.Type()), nil
		}
		t := rv.Type()
		v, tracked := atomicVisit{}, false
		if t.Key().Size() > 0 || t.Elem().Size() > 0 {
			v, tracked = atomicVisit{t, uintptr(rv.UnsafePointer())}, true
			if c.inStack[v] {
				return reflect.Value{}, atomicCycleError(t)
			}
			if cl, ok := c.done[v]; ok {
				return cl, nil
			}
			c.inStack[v] = true
		}
		nv := reflect.MakeMapWithSize(t, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			k, err := c.clone(iter.Key())
			if err != nil {
				return reflect.Value{}, err
			}
			val, err := c.clone(iter.Value())
			if err != nil {
				return reflect.Value{}, err
			}
			nv.SetMapIndex(k, val)
		}
		if tracked {
			delete(c.inStack, v)
			c.done[v] = nv
		}
		return nv, nil

	case reflect.Slice:
		if rv.IsNil() {
			return reflect.Zero(rv.Type()), nil
		}
		t := rv.Type()
		v, tracked := atomicVisit{}, false
		if t.Elem().Size() > 0 && rv.Len() > 0 {
			v, tracked = atomicVisit{t, uintptr(rv.UnsafePointer())}, true
			if c.inStack[v] {
				return reflect.Value{}, atomicCycleError(t)
			}
			if cl, ok := c.done[v]; ok {
				return cl, nil
			}
			c.inStack[v] = true
		}
		nv := reflect.MakeSlice(t, rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			elem, err := c.clone(rv.Index(i))
			if err != nil {
				return reflect.Value{}, err
			}
			nv.Index(i).Set(elem)
		}
		if tracked {
			delete(c.inStack, v)
			c.done[v] = nv
		}
		return nv, nil

	case reflect.Array:
		nv := reflect.New(rv.Type()).Elem()
		for i := 0; i < rv.Len(); i++ {
			elem, err := c.clone(rv.Index(i))
			if err != nil {
				return reflect.Value{}, err
			}
			nv.Index(i).Set(elem)
		}
		return nv, nil

	case reflect.Struct:
		if rv.Type() == atomicTimeType {
			return rv, nil
		}
		nv := reflect.New(rv.Type()).Elem()
		nv.Set(rv)
		for i := 0; i < rv.NumField(); i++ {
			if rv.Type().Field(i).PkgPath != "" { // Unexported.
				continue
			}
			elem, err := c.clone(rv.Field(i))
			if err != nil {
				return reflect.Value{}, err
			}
			nv.Field(i).Set(elem)
		}
		return nv, nil

	case reflect.Interface:
		if rv.IsNil() {
			return reflect.Zero(rv.Type()), nil
		}
		// The concrete value may hide a custom decoder behind an "any"
		// field, which the static type check cannot see.
		if err := checkAtomicType(rv.Elem().Type(), make(map[reflect.Type]bool)); err != nil {
			return reflect.Value{}, err
		}
		elem, err := c.clone(rv.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		nv := reflect.New(rv.Type()).Elem()
		nv.Set(elem)
		return nv, nil
	}

	// Basic types and anything else (chan, func, ...) are copied as-is; the
	// decoder never modifies them in place.
	return rv, nil
}

func atomicCycleError(t reflect.Type) error {
	return fmt.Errorf("toml: DecodeAtomic does not support cyclic data structures (cycle through %s)", t)
}
