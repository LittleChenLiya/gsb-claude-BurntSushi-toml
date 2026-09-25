package toml

import (
	"fmt"
	"reflect"
	"sort"
	"time"
)

// NodeKind describes the shape of a TOML value in a [Contract].
type NodeKind string

// Kinds of nodes in a [Contract].
const (
	// KindObject is a TOML table described by a Go struct.
	KindObject NodeKind = "object"
	// KindMap is a TOML table described by a Go map; the value type is kept
	// in the Elem node.
	KindMap NodeKind = "map"
	// KindArray is a TOML array of scalar (or otherwise non-table) values.
	KindArray NodeKind = "array"
	// KindTableArray is a TOML array of tables: a Go slice or array of
	// structs, maps, or references to them.
	KindTableArray NodeKind = "table-array"
	// KindScalar is a TOML string, integer, float, or boolean.
	KindScalar NodeKind = "scalar"
	// KindDatetime is a TOML datetime, including local datetimes, local
	// dates, and local times; all of them decode into [time.Time].
	KindDatetime NodeKind = "datetime"
	// KindAny is any TOML value (a Go "any"/interface{} or [Primitive]).
	KindAny NodeKind = "any"
	// KindRef is a reference to a type definition in [Contract.Refs], used
	// to express recursive types without infinite expansion.
	KindRef NodeKind = "ref"
	// KindOpaque is a value decoded through a custom [Unmarshaler] that
	// does not provide a [TypeDescriber] callback, or a Go type that has
	// no TOML representation. Its structure is unknown.
	KindOpaque NodeKind = "opaque"
)

// Node describes one value in a configuration contract. It is serializable;
// all fields are exported and carry JSON tags.
type Node struct {
	Kind NodeKind `json:"kind"`

	// TOMLType is the TOML type for KindScalar and KindDatetime nodes:
	// "String", "Integer", "Float", "Bool", or "Datetime".
	TOMLType string `json:"tomlType,omitempty"`

	// GoType is the Go type this node was derived from, for example
	// "time.Time" or "main.Config". It is a stable type identity and never
	// contains a memory address.
	GoType string `json:"goType,omitempty"`

	// Fields lists the keys of a KindObject node, in Go field declaration
	// order.
	Fields []Field `json:"fields,omitempty"`

	// Elem describes the element type of KindArray and KindTableArray
	// nodes, and the value type of KindMap nodes.
	Elem *Node `json:"elem,omitempty"`

	// Ref is the ID of the referenced type definition in [Contract.Refs]
	// for KindRef nodes. IDs are derived from the type identity (package
	// path and name) and are stable across runs.
	Ref string `json:"ref,omitempty"`

	// Required reports whether this value is required. Zero values are not
	// treated as required; requiredness only comes from the explicit field
	// paths in [DescribeOptions.Required].
	Required bool `json:"required,omitempty"`

	// Note holds optional human-readable extra information, for example
	// that a scalar is decoded through encoding.TextUnmarshaler.
	Note string `json:"note,omitempty"`
}

// Field describes a single key of an object node.
type Field struct {
	// Key is the TOML key as a single path segment, exactly as Decode
	// resolves it from the field name or toml tag. It may contain dots
	// (from a tag such as toml:"a.b"); it is still one path segment and
	// corresponds to a quoted TOML key.
	Key string `json:"key"`

	// Name is the name of the Go struct field.
	Name string `json:"name"`

	// Node describes the value stored under this key.
	Node *Node `json:"node"`
}

// Contract is a serializable description of the TOML configuration a Go type
// accepts, suitable for editor tooling that hints key names and types.
type Contract struct {
	// Root describes the top-level TOML document.
	Root *Node `json:"root"`

	// Refs holds the type definitions referenced by KindRef nodes, keyed by
	// reference ID. It only contains types involved in recursion.
	Refs map[string]*Node `json:"refs,omitempty"`

	// Diagnostics lists keys that Decode would not map to any field, for
	// example keys dropped due to conflicting embedded fields. The
	// diagnostics are computed with the same field resolution as Decode,
	// so they are consistent with the decode mapping.
	Diagnostics []string `json:"diagnostics,omitempty"`
}

// DescribeOptions controls [DescribeType].
type DescribeOptions struct {
	// Required lists field paths that are required. Each path is a [Key]:
	// one element per path segment, so keys containing dots stay a single
	// segment. Requiredness is never inferred from zero values or tags; it
	// only comes from this explicit set.
	Required []Key
}

// TypeDescriber may be implemented by types that implement [Unmarshaler] to
// describe the TOML structure they accept. DescribeType then uses the
// returned node instead of marking the type opaque.
//
// The method must be callable on a zero value and must not decode anything;
// DescribeType never calls UnmarshalTOML.
type TypeDescriber interface {
	DescribeTOMLType() *Node
}

var (
	timeValueType = reflect.TypeOf(time.Time{})
	durationType  = reflect.TypeOf(time.Duration(0))
)

// DescribeType derives a configuration [Contract] from a Go type, using the
// same field resolution rules as [Decoder.Decode]: toml tags, anonymous
// (embedded) fields, and nested pointer rules are handled identically, and
// field conflicts are reported in [Contract.Diagnostics].
//
// v may be a zero value of the type to describe, or a pointer to one, for
// example DescribeType(Config{}) or DescribeType((*Config)(nil)).
//
// Recursive types are expressed with KindRef nodes pointing into
// Contract.Refs, so description always terminates. Custom [Unmarshaler]
// types are marked KindOpaque unless they implement [TypeDescriber]; their
// UnmarshalTOML method is never called.
func DescribeType(v any, opts *DescribeOptions) (*Contract, error) {
	t := reflect.TypeOf(v)
	if t == nil {
		return nil, fmt.Errorf("toml: cannot describe nil value")
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	d := &describer{
		building: make(map[reflect.Type]*refState),
		refs:     make(map[string]*Node),
		required: make(map[string]struct{}),
		seenDiag: make(map[string]struct{}),
	}
	if opts != nil {
		for _, k := range opts.Required {
			if len(k) > 0 {
				d.required[k.String()] = struct{}{}
			}
		}
	}

	c := &Contract{Root: d.describe(t, nil)}
	if len(d.refs) > 0 {
		c.Refs = d.refs
	}
	c.Diagnostics = d.diags
	return c, nil
}

// refState tracks a named struct type currently being described.
type refState struct {
	id         string
	referenced bool
}

type describer struct {
	building map[reflect.Type]*refState
	refs     map[string]*Node
	required map[string]struct{}
	seenDiag map[string]struct{}
	diags    []string
}

// typeRefID maps a type identity to a stable reference ID. It never contains
// a memory address.
func typeRefID(t reflect.Type) string {
	if name := t.Name(); name != "" {
		if path := t.PkgPath(); path != "" {
			return path + "." + name
		}
		return name
	}
	return t.String()
}

// describe returns the node for Go type t at the given field path.
func (d *describer) describe(t reflect.Type, path Key) *Node {
	node := d.describeInner(t, path)
	if len(path) > 0 {
		if _, ok := d.required[path.String()]; ok {
			node.Required = true
		}
	}
	return node
}

func (d *describer) describeInner(t reflect.Type, path Key) *Node {
	// Follow pointers, mirroring the indirect() rules used by Decode: stop
	// at types implementing an interface of interest.
	for t.Kind() == reflect.Pointer {
		if t.Implements(unmarshalToml) || t.Implements(unmarshalText) {
			break
		}
		t = t.Elem()
	}

	// A Primitive value can hold any undecoded TOML value.
	if t == primitiveType {
		return &Node{Kind: KindAny, GoType: t.String()}
	}

	// Types that describe themselves. This takes precedence over the
	// opaque Unmarshaler handling below, and never triggers UnmarshalTOML.
	if dt, ok := implementsType(t, describerType); ok {
		v := reflect.New(dt)
		if n := v.Interface().(TypeDescriber).DescribeTOMLType(); n != nil {
			return n
		}
		return &Node{Kind: KindOpaque, GoType: t.String(), Note: "DescribeTOMLType returned nil"}
	}

	// Custom Unmarshaler without a describe callback: opaque. Decode would
	// call UnmarshalTOML; we must not, so the structure is unknown.
	if ut, ok := implementsType(t, unmarshalToml); ok {
		return &Node{
			Kind:   KindOpaque,
			GoType: ut.String(),
			Note:   "custom toml.Unmarshaler without toml.TypeDescriber",
		}
	}

	// time.Time covers TOML datetimes, including local datetimes, local
	// dates, and local times.
	if t == timeValueType {
		return &Node{Kind: KindDatetime, TOMLType: tomlDatetime.typeString(), GoType: t.String()}
	}
	if t == durationType {
		return &Node{
			Kind:     KindScalar,
			TOMLType: tomlInteger.typeString(),
			GoType:   t.String(),
			Note:     "time.Duration: integer nanoseconds or a duration string",
		}
	}

	// TextUnmarshaler accepts any primitive TOML value as text. This check
	// comes after the time.Time check above: *time.Time implements
	// encoding.TextUnmarshaler, but TOML datetimes map to it directly.
	if _, ok := implementsType(t, unmarshalText); ok {
		return &Node{
			Kind:     KindScalar,
			TOMLType: tomlString.typeString(),
			GoType:   t.String(),
			Note:     "decoded through encoding.TextUnmarshaler",
		}
	}

	switch t.Kind() {
	case reflect.Struct:
		return d.describeStruct(t, path)
	case reflect.Map:
		return d.describeMap(t, path)
	case reflect.Slice, reflect.Array:
		return d.describeSlice(t, path)
	case reflect.String:
		return &Node{Kind: KindScalar, TOMLType: tomlString.typeString(), GoType: t.String()}
	case reflect.Bool:
		return &Node{Kind: KindScalar, TOMLType: tomlBool.typeString(), GoType: t.String()}
	case reflect.Float32, reflect.Float64:
		return &Node{Kind: KindScalar, TOMLType: tomlFloat.typeString(), GoType: t.String()}
	case reflect.Interface:
		if t.NumMethod() == 0 {
			return &Node{Kind: KindAny, GoType: t.String()}
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Node{Kind: KindScalar, TOMLType: tomlInteger.typeString(), GoType: t.String()}
	}

	d.diagf("unsupported type %s: no TOML representation", t)
	return &Node{Kind: KindOpaque, GoType: t.String(), Note: "unsupported Go type"}
}

var describerType = reflect.TypeOf((*TypeDescriber)(nil)).Elem()

// implementsType reports whether t, or a pointer to t, implements iface. The
// second return value is the type that implements it (t or *t), mirroring how
// indirect() takes the address of settable values during Decode.
func implementsType(t, iface reflect.Type) (reflect.Type, bool) {
	if t.Implements(iface) {
		return t, true
	}
	if pt := reflect.PointerTo(t); pt.Implements(iface) {
		return pt, true
	}
	return nil, false
}

func (d *describer) describeStruct(t reflect.Type, path Key) *Node {
	// Recursive named types become references; only types actually involved
	// in recursion get an entry in Contract.Refs.
	if t.Name() != "" {
		if st, ok := d.building[t]; ok {
			st.referenced = true
			return &Node{Kind: KindRef, Ref: st.id, GoType: t.String()}
		}
		st := &refState{id: typeRefID(t)}
		d.building[t] = st
		node := d.buildObject(t, path)
		delete(d.building, t)
		if st.referenced {
			d.refs[st.id] = node
			return &Node{Kind: KindRef, Ref: st.id, GoType: t.String()}
		}
		return node
	}
	return d.buildObject(t, path)
}

func (d *describer) buildObject(t reflect.Type, path Key) *Node {
	fields, conflicts := cachedTypeFieldsConflicts(t)
	for _, name := range conflicts {
		d.diagf("%s: key %q dropped: conflicting fields at the same embedding depth (matches Decode)",
			t, name)
	}

	node := &Node{Kind: KindObject, GoType: t.String()}
	for _, f := range fields {
		node.Fields = append(node.Fields, Field{
			Key:  f.name, // Single path segment; may contain dots from a tag.
			Name: t.FieldByIndex(f.index).Name,
			Node: d.describe(f.typ, appendKey(path, f.name)),
		})
	}
	return node
}

func (d *describer) describeMap(t reflect.Type, path Key) *Node {
	keyKind := t.Key().Kind()
	if keyKind != reflect.String && keyKind != reflect.Interface {
		d.diagf("%s: cannot decode to a map with non-string key type", t)
		return &Node{Kind: KindOpaque, GoType: t.String(), Note: "map with non-string key type"}
	}
	return &Node{
		Kind:   KindMap,
		GoType: t.String(),
		Elem:   d.describe(t.Elem(), path),
	}
}

func (d *describer) describeSlice(t reflect.Type, path Key) *Node {
	elem := d.describe(t.Elem(), path)
	kind := KindArray
	switch elem.Kind {
	case KindObject, KindMap, KindRef:
		kind = KindTableArray
	}
	return &Node{Kind: kind, GoType: t.String(), Elem: elem}
}

func (d *describer) diagf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if _, ok := d.seenDiag[msg]; ok {
		return
	}
	d.seenDiag[msg] = struct{}{}
	d.diags = append(d.diags, msg)
}

// appendKey appends a single path segment. The segment is kept as-is even if
// it contains dots, matching how Decode tracks key context.
func appendKey(k Key, seg string) Key {
	out := make(Key, len(k)+1)
	copy(out, k)
	out[len(k)] = seg
	return out
}

// Keys returns all field paths in the contract in declaration order, one
// [Key] per node. Map values and array elements contribute their own path
// only. It is a convenience for editor tooling and tests.
func (c *Contract) Keys() []Key {
	var keys []Key
	var walk func(n *Node, path Key, depth int)
	walk = func(n *Node, path Key, depth int) {
		if n == nil || depth > 64 {
			return
		}
		if n.Kind == KindRef && c.Refs != nil {
			if target, ok := c.Refs[n.Ref]; ok && target != nil {
				n = target
			}
		}
		switch n.Kind {
		case KindObject:
			for _, f := range n.Fields {
				p := appendKey(path, f.Key)
				keys = append(keys, p)
				walk(f.Node, p, depth+1)
			}
		case KindMap, KindArray, KindTableArray:
			walk(n.Elem, path, depth+1)
		}
	}
	walk(c.Root, nil, 0)
	return keys
}

// SortedRefIDs returns the reference IDs in [Contract.Refs] in sorted order,
// for stable display.
func (c *Contract) SortedRefIDs() []string {
	ids := make([]string, 0, len(c.Refs))
	for id := range c.Refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
