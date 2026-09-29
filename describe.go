package toml

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

// NodeKind describes the shape of a [Node] in a [Contract].
type NodeKind string

// Node kinds produced by [DescribeType].
const (
	// KindObject is a TOML table described by a Go struct.
	KindObject NodeKind = "object"
	// KindTableArray is a TOML array of tables: a Go slice or array of
	// structs or maps.
	KindTableArray NodeKind = "tableArray"
	// KindArray is a plain TOML array.
	KindArray NodeKind = "array"
	// KindMap is a TOML table described by a Go map; the value type is
	// preserved in Elem.
	KindMap NodeKind = "map"
	// KindScalar is a TOML scalar; Type holds the TOML type name.
	KindScalar NodeKind = "scalar"
	// KindRef is a reference to a recursive type definition; Ref holds the
	// ID of a node in [Contract.Refs].
	KindRef NodeKind = "ref"
	// KindOpaque is a type implementing [Unmarshaler] without a
	// [TypeDescriber] describe callback; its structure is unknown.
	KindOpaque NodeKind = "opaque"
	// KindAny accepts any TOML value (for example any or [Primitive]).
	KindAny NodeKind = "any"
)

// Scalar type names used in [Node.Type] when Kind is [KindScalar]. These
// match the TOML type names reported by [MetaData.Type], with three
// additions: "Duration" for [time.Duration] (an integer of nanoseconds or a
// [time.ParseDuration] string), "Number" for [encoding/json.Number] and
// "Text" for types implementing [encoding.TextUnmarshaler] (any primitive
// value).
//
// [time.Time] is annotated as "Datetime"; local datetimes, local dates and
// local times also decode into [time.Time] and share this annotation.
const (
	ScalarInteger  = "Integer"
	ScalarFloat    = "Float"
	ScalarString   = "String"
	ScalarBool     = "Bool"
	ScalarDatetime = "Datetime"
	ScalarDuration = "Duration"
	ScalarNumber   = "Number"
	ScalarText     = "Text"
)

// Node describes one value in a TOML configuration contract. It is a
// serializable value: marshalling a [Contract] to JSON or TOML produces a
// document an editor can use to offer key and type hints.
type Node struct {
	Kind NodeKind `json:"kind" toml:"kind"`

	// Type is the TOML scalar type name; set only for KindScalar.
	Type string `json:"type,omitempty" toml:"type,omitempty"`

	// GoType is the stable identity of the Go type
	// (reflect.Type.String); it never contains memory addresses.
	GoType string `json:"goType,omitempty" toml:"goType,omitempty"`

	// ID identifies a recursive type definition; set on the defining node
	// and registered in [Contract.Refs].
	ID string `json:"id,omitempty" toml:"id,omitempty"`

	// Ref is the ID of the definition this node refers to; set only for
	// KindRef.
	Ref string `json:"ref,omitempty" toml:"ref,omitempty"`

	// Fields holds the keys of a KindObject node, in Go field declaration
	// order.
	Fields []Field `json:"fields,omitempty" toml:"fields,omitempty"`

	// Elem describes the element of KindArray, KindTableArray and KindMap
	// nodes.
	Elem *Node `json:"elem,omitempty" toml:"elem,omitempty"`
}

// Field is a single key of an object node.
type Field struct {
	// Name is the TOML key as a single path segment, exactly as Decode
	// resolves it from the field name and toml tag. It may contain dots: a
	// tag such as toml:"a.b" stays one segment and is not split into a
	// nested path.
	Name string `json:"name" toml:"name"`

	// Quoted is Name as it appears in a TOML document, quoted if needed.
	Quoted string `json:"quoted" toml:"quoted"`

	// Required reports whether the caller listed this field's path in
	// [DescribeConfig.Required]. Zero values are never treated as required
	// implicitly.
	Required bool `json:"required,omitempty" toml:"required,omitempty"`

	// Node describes the value stored under this key.
	Node *Node `json:"node" toml:"node"`
}

// FieldConflict reports a TOML key name that Decode cannot map because Go
// fields conflict at the same embedding level. The diagnostics are computed
// with the exact rules Decode uses, so they always agree with the decoder's
// mapping.
type FieldConflict struct {
	// Path is the key path of the struct containing the conflict.
	Path Key `json:"path,omitempty" toml:"path,omitempty"`
	// Name is the TOML key name that was dropped.
	Name string `json:"name" toml:"name"`
	// Fields lists the conflicting Go fields, e.g. "Embedded.X".
	Fields []string `json:"fields" toml:"fields"`
}

// Contract is the result of [DescribeType]: a serializable description of
// the TOML configuration a Go type accepts.
type Contract struct {
	Root *Node `json:"root" toml:"root"`

	// Refs holds the definitions of recursive types, keyed by their ID.
	// Every KindRef node in the contract resolves to an entry here.
	Refs map[string]*Node `json:"refs,omitempty" toml:"refs,omitempty"`

	// Conflicts lists key names dropped due to Go field conflicts.
	Conflicts []FieldConflict `json:"conflicts,omitempty" toml:"conflicts,omitempty"`

	// Required echoes the required paths from [DescribeConfig].
	Required []Key `json:"required,omitempty" toml:"required,omitempty"`
}

// TypeDescriber can be implemented by types that implement [Unmarshaler] to
// describe their own structure in a [Contract]. Without it, such types are
// marked [KindOpaque] and their structure is not inspected.
//
// DescribeTOMLType is called on a zero value and must not dereference
// pointer receivers or call UnmarshalTOML.
type TypeDescriber interface {
	DescribeTOMLType() Node
}

// DescribeConfig configures [DescribeType].
type DescribeConfig struct {
	// Required lists field paths that are required. Nothing is required by
	// default: a zero value in Go does not imply the key must be present.
	Required []Key
}

var (
	describerType = reflect.TypeOf((*TypeDescriber)(nil)).Elem()
	durationType  = reflect.TypeOf(time.Duration(0))
	jsonNumType   = reflect.TypeOf(json.Number(""))
)

// DescribeType derives a configuration [Contract] from a Go type, using the
// same field resolution rules as [Decoder.Decode]: toml tags, anonymous
// (embedded) fields and nested pointers are handled identically, so the
// exported keys match what Decode accepts.
//
// v may be a value of the type to describe, a pointer to it, or a
// [reflect.Type]. Recursive types are expressed with [KindRef] nodes
// referencing definitions in [Contract.Refs] and are never expanded
// infinitely. Types implementing [Unmarshaler] are marked [KindOpaque]
// unless they also implement [TypeDescriber]; their UnmarshalTOML method is
// never called.
func DescribeType(v any, cfg ...DescribeConfig) (*Contract, error) {
	t, ok := v.(reflect.Type)
	if !ok {
		t = reflect.TypeOf(v)
	}
	if t == nil {
		return nil, fmt.Errorf("toml: cannot describe nil value")
	}

	var c DescribeConfig
	if len(cfg) > 0 {
		c = cfg[0]
	}
	d := &describer{
		required:   make(map[string]struct{}, len(c.Required)),
		inProgress: make(map[reflect.Type]bool),
		needRef:    make(map[reflect.Type]bool),
		refs:       make(map[string]*Node),
	}
	for _, k := range c.Required {
		d.required[k.String()] = struct{}{}
	}

	root, err := d.nodeFor(t)
	if err != nil {
		return nil, err
	}
	contract := &Contract{
		Root:      root,
		Conflicts: d.conflicts,
		Required:  c.Required,
	}
	if len(d.refs) > 0 {
		contract.Refs = d.refs
	}
	return contract, nil
}

type describer struct {
	required   map[string]struct{}
	inProgress map[reflect.Type]bool // types currently being expanded
	needRef    map[reflect.Type]bool // types referenced recursively
	refs       map[string]*Node
	conflicts  []FieldConflict
	path       Key
}

// refID maps a type identity to a stable reference ID. It is derived purely
// from the type and never contains memory addresses.
func refID(t reflect.Type) string { return t.String() }

func (d *describer) nodeFor(t reflect.Type) (*Node, error) {
	// Follow pointers, mirroring indirect(): interface implementations are
	// checked at every level, including pointer-to-type.
	for {
		if t == primitiveType {
			return &Node{Kind: KindAny, GoType: t.String()}, nil
		}
		if t.Implements(describerType) || reflect.PointerTo(t).Implements(describerType) {
			return describeViaCallback(t)
		}
		if t.Implements(unmarshalToml) || reflect.PointerTo(t).Implements(unmarshalToml) {
			return &Node{Kind: KindOpaque, GoType: t.String()}, nil
		}
		if t == timeType {
			return &Node{Kind: KindScalar, Type: ScalarDatetime, GoType: t.String()}, nil
		}
		if t.Implements(unmarshalText) || reflect.PointerTo(t).Implements(unmarshalText) {
			return &Node{Kind: KindScalar, Type: ScalarText, GoType: t.String()}, nil
		}
		if t.Kind() != reflect.Pointer {
			break
		}
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.Bool:
		return scalar(t, ScalarBool), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if t == durationType {
			return scalar(t, ScalarDuration), nil
		}
		return scalar(t, ScalarInteger), nil
	case reflect.Float32, reflect.Float64:
		return scalar(t, ScalarFloat), nil
	case reflect.String:
		if t == jsonNumType {
			return scalar(t, ScalarNumber), nil
		}
		return scalar(t, ScalarString), nil
	case reflect.Struct:
		return d.describeStruct(t)
	case reflect.Map:
		return d.describeMap(t)
	case reflect.Slice, reflect.Array:
		return d.describeSlice(t)
	case reflect.Interface:
		if t.NumMethod() > 0 {
			return nil, fmt.Errorf("toml: unsupported type %s", t)
		}
		return &Node{Kind: KindAny, GoType: t.String()}, nil
	}
	return nil, fmt.Errorf("toml: unsupported type %s", t.Kind())
}

func scalar(t reflect.Type, typ string) *Node {
	return &Node{Kind: KindScalar, Type: typ, GoType: t.String()}
}

// describeViaCallback invokes the TypeDescriber callback on a zero value;
// UnmarshalTOML is never called.
func describeViaCallback(t reflect.Type) (*Node, error) {
	if t.Implements(describerType) {
		if td, ok := reflect.Zero(t).Interface().(TypeDescriber); ok {
			n := td.DescribeTOMLType()
			return &n, nil
		}
	}
	v := reflect.New(t)
	if td, ok := v.Interface().(TypeDescriber); ok {
		n := td.DescribeTOMLType()
		return &n, nil
	}
	return nil, fmt.Errorf("toml: %s implements TypeDescriber but the callback could not be called", t)
}

func (d *describer) describeStruct(t reflect.Type) (*Node, error) {
	if d.inProgress[t] {
		d.needRef[t] = true
		return &Node{Kind: KindRef, Ref: refID(t), GoType: t.String()}, nil
	}
	d.inProgress[t] = true

	n := &Node{Kind: KindObject, GoType: t.String()}
	fields, conflicts := typeFieldsDiag(t)
	for _, c := range conflicts {
		c.Path = append(Key(nil), d.path...)
		d.conflicts = append(d.conflicts, c)
	}
	for _, f := range fields {
		d.path = append(d.path, f.name)
		child, err := d.nodeFor(f.typ)
		if err != nil {
			return nil, err
		}
		fn := Field{
			Name:   f.name,
			Quoted: Key{f.name}.maybeQuoted(0),
			Node:   child,
		}
		if _, ok := d.required[d.path.String()]; ok {
			fn.Required = true
		}
		n.Fields = append(n.Fields, fn)
		d.path = d.path[:len(d.path)-1]
	}

	delete(d.inProgress, t)
	if d.needRef[t] {
		n.ID = refID(t)
		d.refs[n.ID] = n
	}
	return n, nil
}

func (d *describer) describeMap(t reflect.Type) (*Node, error) {
	keyKind := t.Key().Kind()
	if keyKind != reflect.String && keyKind != reflect.Interface {
		return nil, fmt.Errorf("toml: cannot describe a map with non-string key type (%s in %q)",
			keyKind, t)
	}
	if d.inProgress[t] {
		d.needRef[t] = true
		return &Node{Kind: KindRef, Ref: refID(t), GoType: t.String()}, nil
	}
	d.inProgress[t] = true
	elem, err := d.nodeFor(t.Elem())
	if err != nil {
		return nil, err
	}
	delete(d.inProgress, t)
	n := &Node{Kind: KindMap, GoType: t.String(), Elem: elem}
	if d.needRef[t] {
		n.ID = refID(t)
		d.refs[n.ID] = n
	}
	return n, nil
}

func (d *describer) describeSlice(t reflect.Type) (*Node, error) {
	elem, err := d.nodeFor(t.Elem())
	if err != nil {
		return nil, err
	}
	kind := KindArray
	if elem.Kind == KindObject || elem.Kind == KindMap || elem.Kind == KindRef {
		kind = KindTableArray
	}
	return &Node{Kind: kind, GoType: t.String(), Elem: elem}, nil
}
