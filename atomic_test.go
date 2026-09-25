package toml

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A type error at the end of otherwise valid TOML data must leave the
// original value and everything reachable from it completely untouched.
func TestDecodeAtomicFailurePreservesOriginal(t *testing.T) {
	type Inner struct {
		Tags map[string]string
		List []int
	}
	type Config struct {
		Name string
		Meta map[string][]int
		Deep *Inner
		Port int
	}

	cfg := Config{
		Name: "old",
		Meta: map[string][]int{"a": {1, 2, 3}},
		Deep: &Inner{Tags: map[string]string{"k": "v"}, List: []int{7, 8}},
		Port: 1,
	}

	// Keep references to the internals to detect any in-place modification.
	var (
		metaRef  = cfg.Meta
		metaARef = cfg.Meta["a"]
		deepRef  = cfg.Deep
		tagsRef  = cfg.Deep.Tags
		listRef  = cfg.Deep.List
	)

	// Everything up to the last key is valid and would modify the target.
	_, err := DecodeAtomic("name = \"new\"\n"+
		"meta = { a = [9, 9] }\n"+
		"deep = { tags = { k = \"changed\" }, list = [0] }\n"+
		"port = \"not-a-number\"\n", &cfg)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "port") {
		t.Fatalf("expected error to mention the port key: %v", err)
	}

	want := Config{
		Name: "old",
		Meta: map[string][]int{"a": {1, 2, 3}},
		Deep: &Inner{Tags: map[string]string{"k": "v"}, List: []int{7, 8}},
		Port: 1,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("value changed after failed decode:\nhave: %#v\nwant: %#v", cfg, want)
	}

	// The internals must be the very same objects, not just equal copies.
	if cfg.Deep != deepRef {
		t.Error("cfg.Deep pointer changed")
	}
	if fmt.Sprintf("%p", cfg.Meta) != fmt.Sprintf("%p", metaRef) {
		t.Error("cfg.Meta map changed")
	}
	if &cfg.Meta["a"][0] != &metaARef[0] {
		t.Error("cfg.Meta[a] slice changed")
	}
	if &cfg.Deep.List[0] != &listRef[0] {
		t.Error("cfg.Deep.List slice changed")
	}

	// Mutations must not have leaked into the original internals either.
	if metaRef["a"][0] != 1 || tagsRef["k"] != "v" || listRef[0] != 7 {
		t.Error("original internals were modified through shared references")
	}
}

// A nil pointer must stay nil when decoding fails, even if the TOML data
// would have allocated it.
func TestDecodeAtomicNilPointerStaysNilOnFailure(t *testing.T) {
	type Inner struct{ Value int }
	type Config struct {
		Inner *Inner
		Port  int
	}

	var cfg Config
	if _, err := DecodeAtomic("inner = { value = 1 }\nport = \"bad\"\n", &cfg); err == nil {
		t.Fatal("expected an error, got nil")
	}
	if cfg.Inner != nil {
		t.Fatalf("nil pointer was allocated by failed decode: %#v", cfg.Inner)
	}
}

// Successful decodes override covered values, preserve everything else,
// allocate nil pointers, and support TOML's native time type.
func TestDecodeAtomicSuccess(t *testing.T) {
	type Inner struct{ Value int }
	type Config struct {
		Name    string
		Keep    string
		Num     *int
		Inner   *Inner
		When    time.Time
		Labels  []string
		SharedA *int
		SharedB *int
	}

	shared := 42
	cfg := Config{
		Name:    "old",
		Keep:    "keepme",
		Labels:  []string{"a"},
		SharedA: &shared,
		SharedB: &shared,
	}

	md, err := DecodeAtomic("name = \"new\"\n"+
		"num = 5\n"+
		"when = 1979-05-27T07:32:00Z\n"+
		"labels = [\"x\", \"y\"]\n"+
		"\n[inner]\nvalue = 3\n", &cfg)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Name != "new" {
		t.Errorf("Name = %q, want %q", cfg.Name, "new")
	}
	if cfg.Keep != "keepme" {
		t.Errorf("Keep = %q, want it preserved as %q", cfg.Keep, "keepme")
	}
	if cfg.Num == nil || *cfg.Num != 5 {
		t.Errorf("Num = %v, want pointer to 5", cfg.Num)
	}
	if cfg.Inner == nil || cfg.Inner.Value != 3 {
		t.Errorf("Inner = %#v, want pointer to {Value: 3}", cfg.Inner)
	}
	if want := time.Date(1979, 5, 27, 7, 32, 0, 0, time.UTC); !cfg.When.Equal(want) {
		t.Errorf("When = %v, want %v", cfg.When, want)
	}
	if !reflect.DeepEqual(cfg.Labels, []string{"x", "y"}) {
		t.Errorf("Labels = %v, want [x y]", cfg.Labels)
	}
	if cfg.SharedA != cfg.SharedB || *cfg.SharedA != 42 {
		t.Error("shared pointer aliasing was not preserved")
	}

	if !md.IsDefined("name") || !md.IsDefined("inner", "value") {
		t.Error("MetaData.IsDefined does not report decoded keys")
	}
}

// Types implementing UnmarshalTOML or encoding.TextUnmarshaler are rejected
// before any decoding takes place.
func TestDecodeAtomicRejectsCustomUnmarshaler(t *testing.T) {
	t.Run("UnmarshalTOML field", func(t *testing.T) {
		cfg := struct {
			Name string
			C    atomicCustomTOML
		}{Name: "orig"}
		_, err := DecodeAtomic("name = \"new\"\n", &cfg)
		if err == nil || !strings.Contains(err.Error(), "DecodeAtomic does not support") {
			t.Fatalf("expected DecodeAtomic rejection, got: %v", err)
		}
		if cfg.Name != "orig" {
			t.Fatalf("target modified before rejection: %#v", cfg)
		}
	})

	t.Run("nil pointer to UnmarshalTOML", func(t *testing.T) {
		cfg := struct{ C *atomicCustomTOML }{}
		if _, err := DecodeAtomic("c = {}\n", &cfg); err == nil ||
			!strings.Contains(err.Error(), "DecodeAtomic does not support") {
			t.Fatalf("expected DecodeAtomic rejection, got: %v", err)
		}
	})

	t.Run("TextUnmarshaler field", func(t *testing.T) {
		cfg := struct{ C atomicCustomText }{}
		if _, err := DecodeAtomic("c = \"x\"\n", &cfg); err == nil ||
			!strings.Contains(err.Error(), "DecodeAtomic does not support") {
			t.Fatalf("expected DecodeAtomic rejection, got: %v", err)
		}
	})

	t.Run("custom type at root", func(t *testing.T) {
		var c atomicCustomTOML
		if _, err := DecodeAtomic("v = 1\n", &c); err == nil ||
			!strings.Contains(err.Error(), "DecodeAtomic does not support") {
			t.Fatalf("expected DecodeAtomic rejection, got: %v", err)
		}
	})

	t.Run("custom type hidden behind any", func(t *testing.T) {
		cfg := struct{ A any }{A: &atomicCustomText{}}
		if _, err := DecodeAtomic("a = \"x\"\n", &cfg); err == nil ||
			!strings.Contains(err.Error(), "DecodeAtomic does not support") {
			t.Fatalf("expected DecodeAtomic rejection, got: %v", err)
		}
	})

	// Sanity check: plain Decode still supports all of the above.
	t.Run("Decode still supports custom unmarshalers", func(t *testing.T) {
		cfg := struct{ C atomicCustomTOML }{}
		if _, err := Decode("c = { v = 1 }\n", &cfg); err != nil {
			t.Fatalf("Decode no longer supports UnmarshalTOML: %v", err)
		}
		if cfg.C.V != "custom" {
			t.Fatalf("UnmarshalTOML was not called by Decode: %#v", cfg.C)
		}
	})
}

// Cyclic data structures are rejected explicitly instead of recursing
// forever.
func TestDecodeAtomicRejectsCycle(t *testing.T) {
	t.Run("pointer cycle", func(t *testing.T) {
		type Node struct {
			Name string
			Next *Node
		}
		a := &Node{Name: "a"}
		b := &Node{Name: "b"}
		a.Next = b
		b.Next = a

		_, err := DecodeAtomic("name = \"x\"\n", a)
		if err == nil || !strings.Contains(err.Error(), "cyclic") {
			t.Fatalf("expected cyclic data error, got: %v", err)
		}
		if a.Name != "a" || a.Next != b || b.Next != a {
			t.Fatalf("target modified: %#v", a)
		}
	})

	t.Run("map cycle", func(t *testing.T) {
		m := map[string]any{"name": "m"}
		m["self"] = m
		_, err := DecodeAtomic("name = \"x\"\n", &m)
		if err == nil || !strings.Contains(err.Error(), "cyclic") {
			t.Fatalf("expected cyclic data error, got: %v", err)
		}
	})

	t.Run("slice cycle", func(t *testing.T) {
		cfg := struct{ Items []any }{}
		cfg.Items = make([]any, 1)
		cfg.Items[0] = cfg.Items
		_, err := DecodeAtomic("items = []\n", &cfg)
		if err == nil || !strings.Contains(err.Error(), "cyclic") {
			t.Fatalf("expected cyclic data error, got: %v", err)
		}
	})

	t.Run("shared references are not cycles", func(t *testing.T) {
		type Config struct {
			A *[]int
			B *[]int
		}
		shared := []int{1}
		cfg := Config{A: &shared, B: &shared}
		if _, err := DecodeAtomic("", &cfg); err != nil {
			t.Fatalf("shared (non-cyclic) references rejected: %v", err)
		}
		if cfg.A != cfg.B {
			t.Fatal("shared pointer aliasing was not preserved")
		}
	})
}

// MetaData must report decoded and undecoded keys exactly like Decode.
func TestDecodeAtomicMetaData(t *testing.T) {
	var cfg struct {
		Name string
		Age  int
	}

	md, err := DecodeAtomic("name = \"x\"\nextra = 1\n", &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !md.IsDefined("name") || !md.IsDefined("extra") {
		t.Error("IsDefined does not report keys from the TOML data")
	}
	if md.IsDefined("nope") {
		t.Error("IsDefined reports a key that does not exist")
	}

	undecoded := md.Undecoded()
	if len(undecoded) != 1 || undecoded[0].String() != "extra" {
		t.Errorf("Undecoded = %v, want [[extra]]", undecoded)
	}

	var keys []string
	for _, k := range md.Keys() {
		keys = append(keys, k.String())
	}
	if !reflect.DeepEqual(keys, []string{"name", "extra"}) {
		t.Errorf("Keys = %v, want [name extra]", keys)
	}

	// MetaData is also returned on failure.
	md, err = DecodeAtomic("name = \"x\"\nage = \"not-a-number\"\n", &cfg)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !md.IsDefined("age") {
		t.Error("MetaData from failed decode does not describe the input")
	}
}

// Decoding into a map at the root is atomic as well.
func TestDecodeAtomicMapRoot(t *testing.T) {
	m := map[string]int{"a": 1}
	ref := m

	if _, err := DecodeAtomic("a = \"not-a-number\"\nb = 2\n", &m); err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !reflect.DeepEqual(m, map[string]int{"a": 1}) {
		t.Fatalf("map changed after failed decode: %v", m)
	}
	if fmt.Sprintf("%p", m) != fmt.Sprintf("%p", ref) {
		t.Fatal("map was replaced after failed decode")
	}

	if _, err := DecodeAtomic("b = 2\n", &m); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m, map[string]int{"a": 1, "b": 2}) {
		t.Fatalf("unexpected map after successful decode: %v", m)
	}
}

type atomicCustomTOML struct{ V string }

func (c *atomicCustomTOML) UnmarshalTOML(any) error { c.V = "custom"; return nil }

type atomicCustomText struct{ V string }

func (c *atomicCustomText) UnmarshalText(b []byte) error { c.V = string(b); return nil }

func ExampleDecodeAtomic() {
	type Config struct {
		Host string
		Port int
		Tags map[string]string
	}

	cfg := Config{Host: "localhost", Port: 8080, Tags: map[string]string{"env": "dev"}}
	fmt.Printf("before:        %+v\n", cfg)

	// Successful decode: Host and Port are replaced, Tags is kept.
	if _, err := DecodeAtomic("host = \"example.com\"\nport = 9090\n", &cfg); err != nil {
		fmt.Println("error:", err)
	}
	fmt.Printf("after success: %+v\n", cfg)

	// Failed decode: port has the wrong type, so nothing is applied and the
	// previous configuration is kept in full.
	_, err := DecodeAtomic("host = \"attacker\"\nport = \"not-a-number\"\n", &cfg)
	fmt.Printf("after failure: %+v (rejected: %v)\n", cfg, err != nil)

	// Output:
	// before:        {Host:localhost Port:8080 Tags:map[env:dev]}
	// after success: {Host:example.com Port:9090 Tags:map[env:dev]}
	// after failure: {Host:example.com Port:9090 Tags:map[env:dev]} (rejected: true)
}
