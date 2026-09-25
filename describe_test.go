package toml

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type descEmbedded struct {
	Host string
	Port int `toml:"port"`
}

type descSub struct {
	Enabled bool
	Weight  float64
}

type descItem struct {
	Label string
}

var descOpaqueCalls int

// descOpaque implements Unmarshaler but not TypeDescriber: it must be
// marked opaque and its UnmarshalTOML must never be called.
type descOpaque struct{}

func (descOpaque) UnmarshalTOML(any) error { descOpaqueCalls++; return nil }

// descSelf implements Unmarshaler and TypeDescriber: DescribeType must use
// the callback and still never call UnmarshalTOML.
type descSelf struct{}

func (descSelf) UnmarshalTOML(any) error { descOpaqueCalls++; return nil }
func (descSelf) DescribeTOMLType() *Node {
	return &Node{Kind: KindScalar, TOMLType: "String", Note: "self-described"}
}

type descConfig struct {
	descEmbedded                   // anonymous fields are promoted
	Name         string            `toml:"renamed"`
	Sub          *descSub          `toml:"sub"`
	Items        []descItem        `toml:"items"`
	Dotted       string            `toml:"a.b"`
	Ignored      string            `toml:"-"`
	Labels       map[string]string `toml:"labels"`
	When         time.Time         `toml:"when"`
	Opaque       descOpaque        `toml:"opaque"`
	Self         descSelf          `toml:"self"`
	unexp        int               //nolint:unused // unexported fields are skipped
}

func fieldKeys(t *testing.T, n *Node) []string {
	t.Helper()
	if n.Kind != KindObject {
		t.Fatalf("expected object node, got %s", n.Kind)
	}
	keys := make([]string, 0, len(n.Fields))
	for _, f := range n.Fields {
		keys = append(keys, f.Key)
	}
	return keys
}

func findField(t *testing.T, n *Node, key string) Field {
	t.Helper()
	for _, f := range n.Fields {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("key %q not found in object node", key)
	return Field{}
}

func TestDescribeContract(t *testing.T) {
	descOpaqueCalls = 0
	c, err := DescribeType(descConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Fields in declaration order; anonymous fields promoted in place;
	// renamed tag used; ignored and unexported fields absent.
	want := []string{"Host", "port", "renamed", "sub", "items", "a.b", "labels", "when", "opaque", "self"}
	if got := fieldKeys(t, c.Root); !equalStrings(got, want) {
		t.Fatalf("keys:\n got: %q\nwant: %q", got, want)
	}

	if f := findField(t, c.Root, "sub"); f.Node.Kind != KindObject || f.Name != "Sub" {
		t.Fatalf("sub: unexpected node %+v", f.Node)
	}
	items := findField(t, c.Root, "items").Node
	if items.Kind != KindTableArray || items.Elem.Kind != KindObject {
		t.Fatalf("items: unexpected node %+v", items)
	}
	labels := findField(t, c.Root, "labels").Node
	if labels.Kind != KindMap || labels.Elem.Kind != KindScalar || labels.Elem.TOMLType != "String" {
		t.Fatalf("labels: map value type not kept: %+v", labels)
	}
	if when := findField(t, c.Root, "when").Node; when.Kind != KindDatetime || when.TOMLType != "Datetime" {
		t.Fatalf("when: unexpected node %+v", when)
	}
	if op := findField(t, c.Root, "opaque").Node; op.Kind != KindOpaque {
		t.Fatalf("opaque: expected opaque node, got %+v", op)
	}
	if sf := findField(t, c.Root, "self").Node; sf.Kind != KindScalar || sf.Note != "self-described" {
		t.Fatalf("self: TypeDescriber callback not used: %+v", sf)
	}
	if descOpaqueCalls != 0 {
		t.Fatalf("UnmarshalTOML called %d times during DescribeType", descOpaqueCalls)
	}

	// The contract must be serializable and must not contain memory
	// addresses.
	js, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(js), "0x") {
		t.Fatalf("contract contains a memory address: %s", js)
	}
}

func equalStrings(a, b []string) bool {
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

// The keys in the contract must be valid for a real Decode, including the
// renamed tag, the anonymous fields, the pointer sub-table, the slice, and
// the dotted key (a single path segment, quoted in TOML).
func TestDescribeKeysDecode(t *testing.T) {
	c, err := DescribeType(descConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = c

	const doc = `Host = "example.com"
port = 8080
renamed = "cfg"
"a.b" = "dotted"
when = 1979-05-27T07:32:00Z
opaque = "anything"
self = "text"
[sub]
Enabled = true
Weight = 1.5
[labels]
env = "prod"
[[items]]
Label = "one"
[[items]]
Label = "two"
`

	var cfg descConfig
	md, err := Decode(doc, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if u := md.Undecoded(); len(u) != 0 {
		t.Fatalf("undecoded keys: %v", u)
	}
	if cfg.Host != "example.com" || cfg.Port != 8080 || cfg.Name != "cfg" ||
		cfg.Dotted != "dotted" || cfg.Sub == nil || !cfg.Sub.Enabled ||
		cfg.Sub.Weight != 1.5 || len(cfg.Items) != 2 || cfg.Items[1].Label != "two" ||
		cfg.Labels["env"] != "prod" || cfg.When.Year() != 1979 {
		t.Fatalf("decoded values wrong: %+v (sub %+v)", cfg, cfg.Sub)
	}
	if cfg.When.Location() != time.UTC {
		t.Fatalf("when: expected UTC, got %v", cfg.When.Location())
	}
}

type descEmbedA struct{ X int }
type descEmbedB struct{ X string }
type descConflict struct {
	descEmbedA
	descEmbedB
	Keep string
}

// Field conflict diagnostics must be consistent with the decode mapping: a
// key dropped from the contract must also be ignored by Decode.
func TestDescribeConflicts(t *testing.T) {
	c, err := DescribeType(descConflict{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := fieldKeys(t, c.Root); !equalStrings(got, []string{"Keep"}) {
		t.Fatalf("keys: got %q, want only Keep", got)
	}
	if len(c.Diagnostics) != 1 || !strings.Contains(c.Diagnostics[0], `"X"`) {
		t.Fatalf("expected one conflict diagnostic for X, got %v", c.Diagnostics)
	}

	var v descConflict
	md, err := Decode("X = 1\nKeep = \"k\"\n", &v)
	if err != nil {
		t.Fatal(err)
	}
	und := md.Undecoded()
	if len(und) != 1 || und[0].String() != "X" {
		t.Fatalf("Decode mapping inconsistent with diagnostics: undecoded %v", und)
	}
	if v.Keep != "k" {
		t.Fatalf("Keep not decoded: %+v", v)
	}
}

type descNode struct {
	Name  string
	Child *descNode
}

type descMutA struct{ B *descMutB }
type descMutB struct{ A *descMutA }

// checkRefs verifies that every ref node points at an existing Refs entry.
func checkRefs(t *testing.T, c *Contract) {
	t.Helper()
	var walk func(n *Node)
	walk = func(n *Node) {
		if n == nil {
			return
		}
		if n.Kind == KindRef {
			target, ok := c.Refs[n.Ref]
			if !ok {
				t.Fatalf("ref %q has no target in Contract.Refs", n.Ref)
			}
			if target.Kind != KindObject {
				t.Fatalf("ref %q target is %s, expected object", n.Ref, target.Kind)
			}
		}
		for _, f := range n.Fields {
			walk(f.Node)
		}
		walk(n.Elem)
	}
	walk(c.Root)
	for _, id := range c.SortedRefIDs() {
		walk(c.Refs[id])
	}
}

func TestDescribeRecursive(t *testing.T) {
	c, err := DescribeType(descNode{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Root.Kind != KindRef {
		t.Fatalf("root: expected ref node, got %+v", c.Root)
	}
	checkRefs(t, c)

	// Reference IDs derive from the type identity, not memory addresses.
	wantID := "github.com/BurntSushi/toml.descNode"
	if c.Root.Ref != wantID {
		t.Fatalf("ref ID: got %q, want %q", c.Root.Ref, wantID)
	}

	// Mutual recursion terminates and all references resolve.
	cm, err := DescribeType(descMutA{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkRefs(t, cm)
	if len(cm.Refs) != 1 {
		t.Fatalf("expected exactly one ref target, got %v", cm.SortedRefIDs())
	}

	// Stable output across runs.
	again, err := DescribeType(descNode{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	j1, err1 := json.Marshal(c)
	j2, err2 := json.Marshal(again)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if string(j1) != string(j2) {
		t.Fatalf("unstable contract:\n%s\n%s", j1, j2)
	}
}

func TestDescribeRequired(t *testing.T) {
	c, err := DescribeType(descConfig{}, &DescribeOptions{
		Required: []Key{{"renamed"}, {"sub", "Enabled"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !findField(t, c.Root, "renamed").Node.Required {
		t.Fatal("renamed should be required")
	}
	sub := findField(t, c.Root, "sub").Node
	if sub.Required {
		t.Fatal("sub itself was not marked required")
	}
	if !findField(t, sub, "Enabled").Node.Required {
		t.Fatal("sub.Enabled should be required")
	}
	// Zero values are not treated as required by default.
	for _, f := range c.Root.Fields {
		if f.Key != "renamed" && f.Node.Required {
			t.Fatalf("%s unexpectedly required", f.Key)
		}
	}
}

func TestDescribeDatetimeAndPrimitive(t *testing.T) {
	type dt struct {
		T time.Time
		P Primitive
		D time.Duration
	}
	c, err := DescribeType(dt{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := findField(t, c.Root, "T").Node; n.Kind != KindDatetime {
		t.Fatalf("T: got %+v", n)
	}
	if n := findField(t, c.Root, "P").Node; n.Kind != KindAny {
		t.Fatalf("P: got %+v", n)
	}
	if n := findField(t, c.Root, "D").Node; n.Kind != KindScalar || n.TOMLType != "Integer" {
		t.Fatalf("D: got %+v", n)
	}
}

func TestDescribeNil(t *testing.T) {
	if _, err := DescribeType(nil, nil); err == nil {
		t.Fatal("expected error for nil value")
	}
}
