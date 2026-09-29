package toml_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

type descEmbedded struct {
	Enabled bool `toml:"enabled"`
}

type descServer struct {
	Host    string `toml:"host"`
	Port    int // default key name "Port"
	Ignored string `toml:"-"`
	hidden  int
}

var unmarshalCalls int

type descCounter struct{ n int }

func (c *descCounter) UnmarshalTOML(v any) error { unmarshalCalls++; return nil }

type descDescribed struct{}

func (descDescribed) DescribeTOMLType() toml.Node {
	return toml.Node{Kind: toml.KindScalar, Type: toml.ScalarString}
}
func (*descDescribed) UnmarshalTOML(v any) error { unmarshalCalls++; return nil }

type descConfig struct {
	descEmbedded
	Name     string                `toml:"name"`
	Dotted   string                `toml:"dotted.key"`
	Tags     []string              `toml:"tags"`
	When     time.Time             `toml:"when"`
	Local    time.Time             `toml:"local"`
	Timeout  time.Duration         `toml:"timeout"`
	Custom   *descCounter          `toml:"custom"`
	Desc     descDescribed         `toml:"desc"`
	Server   *descServer           `toml:"server"`
	Extra    map[string]int        `toml:"extra"`
	Backends map[string]descServer `toml:"backends"`
}

func fieldNames(t *testing.T, n *toml.Node) []string {
	t.Helper()
	var names []string
	for _, f := range n.Fields {
		names = append(names, f.Name)
	}
	return names
}

func findField(t *testing.T, n *toml.Node, name string) toml.Field {
	t.Helper()
	for _, f := range n.Fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("field %q not found in %v", name, fieldNames(t, n))
	return toml.Field{}
}

func TestDescribeContract(t *testing.T) {
	unmarshalCalls = 0
	c, err := toml.DescribeType(descConfig{})
	if err != nil {
		t.Fatal(err)
	}

	// Fields appear in Go declaration order, anonymous fields promoted.
	want := []string{"enabled", "name", "dotted.key", "tags", "when", "local",
		"timeout", "custom", "desc", "server", "extra", "backends"}
	if got := fieldNames(t, c.Root); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("field order:\n got: %v\nwant: %v", got, want)
	}

	// Dotted tag stays a single path segment, quoted for documents.
	dot := findField(t, c.Root, "dotted.key")
	if dot.Quoted != `"dotted.key"` {
		t.Fatalf("dotted key not quoted as single segment: %q", dot.Quoted)
	}

	// Pointer sub-table is unwrapped to an object; slice is an array.
	if srv := findField(t, c.Root, "server").Node; srv.Kind != toml.KindObject {
		t.Fatalf("server: want object, got %s", srv.Kind)
	}
	if tags := findField(t, c.Root, "tags").Node; tags.Kind != toml.KindArray ||
		tags.Elem.Type != toml.ScalarString {
		t.Fatalf("tags: want array of String, got %+v", tags)
	}

	// time.Time (including local datetimes) and time.Duration annotations.
	if when := findField(t, c.Root, "when").Node; when.Kind != toml.KindScalar ||
		when.Type != toml.ScalarDatetime {
		t.Fatalf("when: want Datetime scalar, got %+v", when)
	}
	if loc := findField(t, c.Root, "local").Node; loc.Type != toml.ScalarDatetime {
		t.Fatalf("local: want Datetime scalar, got %+v", loc)
	}
	if dur := findField(t, c.Root, "timeout").Node; dur.Type != toml.ScalarDuration {
		t.Fatalf("timeout: want Duration scalar, got %+v", dur)
	}

	// Maps keep their value type.
	extra := findField(t, c.Root, "extra").Node
	if extra.Kind != toml.KindMap || extra.Elem.Type != toml.ScalarInteger {
		t.Fatalf("extra: want map of Integer, got %+v", extra)
	}
	backends := findField(t, c.Root, "backends").Node
	if backends.Kind != toml.KindMap || backends.Elem.Kind != toml.KindObject ||
		findField(t, backends.Elem, "host").Node.Type != toml.ScalarString {
		t.Fatalf("backends: want map of object, got %+v", backends)
	}

	// Custom Unmarshaler is opaque and was never executed; a TypeDescriber
	// callback is used instead when provided.
	if cu := findField(t, c.Root, "custom").Node; cu.Kind != toml.KindOpaque {
		t.Fatalf("custom: want opaque, got %+v", cu)
	}
	if de := findField(t, c.Root, "desc").Node; de.Kind != toml.KindScalar ||
		de.Type != toml.ScalarString {
		t.Fatalf("desc: want described scalar, got %+v", de)
	}
	if unmarshalCalls != 0 {
		t.Fatalf("UnmarshalTOML called %d times during DescribeType", unmarshalCalls)
	}

	// Ignored and unexported fields are absent.
	for _, name := range fieldNames(t, findField(t, c.Root, "server").Node) {
		if name == "Ignored" || name == "hidden" {
			t.Fatalf("unexpected field %q in server node", name)
		}
	}

	// Nothing is required unless the caller says so.
	for _, f := range c.Root.Fields {
		if f.Required {
			t.Fatalf("field %q required without explicit paths", f.Name)
		}
	}
}

// Every key in the contract must be accepted by the real decoder.
func TestDescribeContractKeysDecode(t *testing.T) {
	unmarshalCalls = 0
	c, err := toml.DescribeType(descConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Root.Kind != toml.KindObject {
		t.Fatalf("root: want object, got %s", c.Root.Kind)
	}
	if unmarshalCalls != 0 {
		t.Fatalf("UnmarshalTOML called %d times during DescribeType", unmarshalCalls)
	}

	doc := `
enabled = true
name = "n"
"dotted.key" = "d"
tags = ["a", "b"]
when = 1979-05-27T07:32:00Z
local = 1979-05-27T07:32:00
timeout = "1s"
custom = "anything"
desc = "x"

[server]
host = "h"
Port = 8080

[extra]
foo = 1

[backends.one]
host = "h1"
Port = 1
`
	var v descConfig
	md, err := toml.Decode(doc, &v)
	if err != nil {
		t.Fatal(err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		t.Fatalf("undecoded keys from contract-derived document: %v", u)
	}

	// Every contract path is defined in the decoded document.
	paths := []toml.Key{
		{"enabled"}, {"name"}, {"dotted.key"}, {"tags"}, {"when"}, {"local"},
		{"timeout"}, {"custom"}, {"desc"},
		{"server", "host"}, {"server", "Port"},
		{"extra", "foo"}, {"backends", "one", "host"},
	}
	for _, p := range paths {
		if !md.IsDefined(p...) {
			t.Errorf("contract key %v not defined after decode", p)
		}
	}
	if v.Dotted != "d" || v.Server.Host != "h" || v.Extra["foo"] != 1 ||
		v.Backends["one"].Port != 1 || !v.Enabled {
		t.Fatalf("decoded values wrong: %+v", v)
	}
}

func TestDescribeRequired(t *testing.T) {
	c, err := toml.DescribeType(descConfig{}, toml.DescribeConfig{
		Required: []toml.Key{{"name"}, {"server", "host"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !findField(t, c.Root, "name").Required {
		t.Error("name should be required")
	}
	if findField(t, c.Root, "tags").Required {
		t.Error("tags should not be required")
	}
	srv := findField(t, c.Root, "server").Node
	if !findField(t, srv, "host").Required {
		t.Error("server.host should be required")
	}
	if findField(t, srv, "Port").Required {
		t.Error("server.Port should not be required")
	}
}

type recNode struct {
	Value string   `toml:"value"`
	Next  *recNode `toml:"next"`
}

type recA struct {
	Name string `toml:"name"`
	B    *recB  `toml:"b"`
}

type recB struct {
	A *recA `toml:"a"`
}

func walkNodes(n *toml.Node, fn func(*toml.Node)) {
	if n == nil {
		return
	}
	fn(n)
	for i := range n.Fields {
		walkNodes(n.Fields[i].Node, fn)
	}
	walkNodes(n.Elem, fn)
}

func checkRefsResolve(t *testing.T, c *toml.Contract) {
	t.Helper()
	walkNodes(c.Root, func(n *toml.Node) {
		if n.Kind != toml.KindRef {
			return
		}
		if n.Ref == "" {
			t.Error("ref node with empty target")
			return
		}
		if _, ok := c.Refs[n.Ref]; !ok {
			t.Errorf("ref target %q missing from contract refs", n.Ref)
		}
		if strings.Contains(n.Ref, "0x") {
			t.Errorf("ref ID %q looks like a memory address", n.Ref)
		}
	})
	for id, def := range c.Refs {
		if def.ID != id {
			t.Errorf("refs[%q].ID = %q", id, def.ID)
		}
	}
}

func TestDescribeRecursive(t *testing.T) {
	c, err := toml.DescribeType(recNode{})
	if err != nil {
		t.Fatal(err)
	}
	next := findField(t, c.Root, "next")
	if next.Node.Kind != toml.KindRef {
		t.Fatalf("next: want ref node, got %+v", next.Node)
	}
	if c.Root.ID == "" {
		t.Fatal("recursive root has no ID")
	}
	if c.Refs[c.Root.ID] != c.Root {
		t.Fatal("root definition not registered in refs")
	}
	checkRefsResolve(t, c)

	// Reference IDs are stable across calls and address-free.
	c2, err := toml.DescribeType(recNode{})
	if err != nil {
		t.Fatal(err)
	}
	if c2.Root.ID != c.Root.ID || findField(t, c2.Root, "next").Node.Ref != next.Node.Ref {
		t.Fatalf("unstable ref IDs: %q vs %q", c2.Root.ID, c.Root.ID)
	}
}

func TestDescribeMutualRecursion(t *testing.T) {
	c, err := toml.DescribeType(recA{})
	if err != nil {
		t.Fatal(err)
	}
	b := findField(t, c.Root, "b").Node
	if b.Kind != toml.KindObject {
		t.Fatalf("b: want inline object, got %+v", b)
	}
	a := findField(t, b, "a").Node
	if a.Kind != toml.KindRef || a.Ref != c.Root.ID {
		t.Fatalf("a: want ref to root %q, got %+v", c.Root.ID, a)
	}
	checkRefsResolve(t, c)
}

type conflictA struct{ X int `toml:"x"` }
type conflictB struct{ X string `toml:"x"` }
type conflictRoot struct {
	conflictA
	conflictB
	Y int `toml:"y"`
}

type domEmbedded struct{ W int }
type domRoot struct {
	domEmbedded
	W2 string `toml:"W"`
}

func TestDescribeConflictsMatchDecode(t *testing.T) {
	c, err := toml.DescribeType(conflictRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if got := fieldNames(t, c.Root); strings.Join(got, ",") != "y" {
		t.Fatalf("conflicting key should be dropped, fields: %v", got)
	}
	if len(c.Conflicts) != 1 || c.Conflicts[0].Name != "x" {
		t.Fatalf("want one conflict for x, got %+v", c.Conflicts)
	}
	if got := strings.Join(c.Conflicts[0].Fields, ","); got != "conflictA.X,conflictB.X" {
		t.Fatalf("conflicting fields: %v", got)
	}

	// The decoder agrees: x is undecodable, y maps fine.
	var v conflictRoot
	md, err := toml.Decode("x = 1\ny = 2\n", &v)
	if err != nil {
		t.Fatal(err)
	}
	u := md.Undecoded()
	if len(u) != 1 || u[0].String() != "x" {
		t.Fatalf("undecoded: %v", u)
	}
	if v.Y != 2 {
		t.Fatalf("y not decoded: %+v", v)
	}

	// A tagged field dominates an embedded one, in contract and decode.
	c2, err := toml.DescribeType(domRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if got := fieldNames(t, c2.Root); strings.Join(got, ",") != "W" {
		t.Fatalf("dominated fields: %v", got)
	}
	if w := findField(t, c2.Root, "W").Node; w.Type != toml.ScalarString {
		t.Fatalf("tagged field should dominate: %+v", w)
	}
	if len(c2.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", c2.Conflicts)
	}
	var v2 domRoot
	if _, err := toml.Decode(`W = "s"`, &v2); err != nil {
		t.Fatal(err)
	}
	if v2.W2 != "s" || v2.domEmbedded.W != 0 {
		t.Fatalf("tagged field did not dominate in decode: %+v", v2)
	}
}

func TestDescribeSerializable(t *testing.T) {
	c, err := toml.DescribeType(recA{})
	if err != nil {
		t.Fatal(err)
	}
	buf, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(buf), "0x") {
		t.Fatalf("contract contains memory addresses: %s", buf)
	}
	var round map[string]any
	if err := json.Unmarshal(buf, &round); err != nil {
		t.Fatalf("contract not serializable: %v", err)
	}
}

