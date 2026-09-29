package toml

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type atomicServer struct {
	Host string
	Port int
}

type atomicConfig struct {
	Name    string
	Tags    []string
	Labels  map[string]string
	Server  *atomicServer
	When    time.Time
	Keep    string
	Numbers [2]int
}

func TestDecodeAtomicFailureKeepsOriginal(t *testing.T) {
	cfg := atomicConfig{
		Name:   "old-name",
		Tags:   []string{"a", "b"},
		Labels: map[string]string{"env": "prod", "nested": "keep"},
		Server: &atomicServer{Host: "old-host", Port: 1},
		Keep:   "untouched",
	}
	// Keep references to the original internals to prove they are not
	// mutated or replaced in-place.
	origTags := cfg.Tags
	origLabels := cfg.Labels
	origServer := cfg.Server

	// The first half is valid and would modify the config; the type error
	// at the end must make the whole decode fail.
	data := `
Name = "new-name"
Tags = ["x", "y", "z"]

[Labels]
env = "staging"

[Server]
Host = "new-host"
Port = "not-a-number"
`
	_, err := DecodeAtomic(data, &cfg)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	// The value must be exactly as before, including the parts the valid
	// first half of the input would have changed.
	if cfg.Name != "old-name" {
		t.Errorf("Name changed to %q", cfg.Name)
	}
	if cfg.Keep != "untouched" {
		t.Errorf("Keep changed to %q", cfg.Keep)
	}
	if !reflect.DeepEqual(cfg.Tags, []string{"a", "b"}) {
		t.Errorf("Tags changed to %v", cfg.Tags)
	}
	if !reflect.DeepEqual(cfg.Labels, map[string]string{"env": "prod", "nested": "keep"}) {
		t.Errorf("Labels changed to %v", cfg.Labels)
	}
	if *cfg.Server != (atomicServer{Host: "old-host", Port: 1}) {
		t.Errorf("Server changed to %+v", cfg.Server)
	}

	// The internal references must be the very same objects: a shallow
	// copy would have leaked the failed decode into them.
	if &cfg.Tags[0] != &origTags[0] {
		t.Error("Tags slice was replaced")
	}
	if reflect.ValueOf(cfg.Labels).Pointer() != reflect.ValueOf(origLabels).Pointer() {
		t.Error("Labels map was replaced")
	}
	if cfg.Server != origServer {
		t.Error("Server pointer was replaced")
	}
}

func TestDecodeAtomicSuccess(t *testing.T) {
	when := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	cfg := atomicConfig{
		Name:    "old-name",
		Tags:    []string{"a"},
		Labels:  map[string]string{"env": "prod"},
		Server:  &atomicServer{Host: "old-host", Port: 1},
		Keep:    "untouched",
		Numbers: [2]int{1, 2},
	}
	origServer := cfg.Server

	data := `
Name = "new-name"
Tags = ["x", "y"]
When = 2024-05-06T07:08:09Z
Numbers = [3, 4]

[Labels]
env = "staging"
team = "core"

[Server]
Host = "new-host"
Port = 8080
`
	md, err := DecodeAtomic(data, &cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Values from the TOML document are overwritten.
	if cfg.Name != "new-name" {
		t.Errorf("Name = %q", cfg.Name)
	}
	if !reflect.DeepEqual(cfg.Tags, []string{"x", "y"}) {
		t.Errorf("Tags = %v", cfg.Tags)
	}
	if !reflect.DeepEqual(cfg.Labels, map[string]string{"env": "staging", "team": "core"}) {
		t.Errorf("Labels = %v", cfg.Labels)
	}
	if *cfg.Server != (atomicServer{Host: "new-host", Port: 8080}) {
		t.Errorf("Server = %+v", cfg.Server)
	}
	if !cfg.When.Equal(when) {
		t.Errorf("When = %v", cfg.When)
	}
	if cfg.Numbers != [2]int{3, 4} {
		t.Errorf("Numbers = %v", cfg.Numbers)
	}

	// Values not covered by the input are preserved.
	if cfg.Keep != "untouched" {
		t.Errorf("Keep = %q", cfg.Keep)
	}

	// The decoded value is a copy: the old Server pointer must not have
	// been mutated or reused.
	if cfg.Server == origServer {
		t.Error("Server pointer was reused; decode mutated the original object graph")
	}
	if *origServer != (atomicServer{Host: "old-host", Port: 1}) {
		t.Errorf("original Server changed to %+v", origServer)
	}

	// MetaData still reports what was read.
	for _, k := range []string{"Name", "Tags", "When", "Numbers", "Labels", "Server"} {
		if !md.IsDefined(k) {
			t.Errorf("IsDefined(%q) = false", k)
		}
	}
	if md.IsDefined("Keep") {
		t.Error("IsDefined(Keep) = true")
	}
}

func TestDecodeAtomicNilPointersAndMaps(t *testing.T) {
	var cfg atomicConfig // Everything nil/zero.

	data := `
Name = "fresh"
Tags = ["one"]

[Labels]
a = "b"

[Server]
Host = "h"
Port = 9
`
	if _, err := DecodeAtomic(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Server == nil || cfg.Server.Host != "h" || cfg.Server.Port != 9 {
		t.Errorf("Server = %+v", cfg.Server)
	}
	if !reflect.DeepEqual(cfg.Labels, map[string]string{"a": "b"}) {
		t.Errorf("Labels = %v", cfg.Labels)
	}
	if !reflect.DeepEqual(cfg.Tags, []string{"one"}) {
		t.Errorf("Tags = %v", cfg.Tags)
	}
}

func TestDecodeAtomicUndecoded(t *testing.T) {
	var cfg struct {
		Name string
	}
	md, err := DecodeAtomic("Name = \"x\"\nExtra = 1\n", &cfg)
	if err != nil {
		t.Fatal(err)
	}
	undecoded := md.Undecoded()
	if len(undecoded) != 1 || undecoded[0].String() != "Extra" {
		t.Errorf("Undecoded() = %v", undecoded)
	}
}

type atomicUnmarshalTOML struct{ called bool }

func (u *atomicUnmarshalTOML) UnmarshalTOML(any) error {
	u.called = true
	return nil
}

type atomicUnmarshalText struct{ called bool }

func (u *atomicUnmarshalText) UnmarshalText([]byte) error {
	u.called = true
	return nil
}

func TestDecodeAtomicRejectsCustomUnmarshalers(t *testing.T) {
	t.Run("UnmarshalTOML", func(t *testing.T) {
		v := &atomicUnmarshalTOML{}
		_, err := DecodeAtomic("a = 1", v)
		if err == nil || !strings.Contains(err.Error(), "DecodeAtomic does not support") {
			t.Fatalf("err = %v", err)
		}
		if v.called {
			t.Error("UnmarshalTOML was called")
		}
	})

	t.Run("TextUnmarshaler", func(t *testing.T) {
		v := &atomicUnmarshalText{}
		_, err := DecodeAtomic("a = 1", v)
		if err == nil || !strings.Contains(err.Error(), "DecodeAtomic does not support") {
			t.Fatalf("err = %v", err)
		}
		if v.called {
			t.Error("UnmarshalText was called")
		}
	})

	t.Run("NestedField", func(t *testing.T) {
		cfg := struct {
			Name   string
			Custom atomicUnmarshalTOML
		}{Name: "keep"}
		_, err := DecodeAtomic("Name = \"changed\"", &cfg)
		if err == nil || !strings.Contains(err.Error(), "DecodeAtomic does not support") {
			t.Fatalf("err = %v", err)
		}
		// Rejected before decoding: even the valid field is untouched.
		if cfg.Name != "keep" {
			t.Errorf("Name changed to %q", cfg.Name)
		}
	})
}

func TestDecodeAtomicRejectsCycles(t *testing.T) {
	t.Run("MapCycle", func(t *testing.T) {
		m := map[string]any{"key": "value"}
		m["self"] = m
		_, err := DecodeAtomic("key = \"new\"", &m)
		if err == nil || !strings.Contains(err.Error(), "cyclic") {
			t.Fatalf("err = %v", err)
		}
		if m["key"] != "value" || m["self"] == nil {
			t.Errorf("map changed: %v", m)
		}
	})

	t.Run("PointerCycle", func(t *testing.T) {
		type node struct {
			Next *node
			Val  int
		}
		n := &node{Val: 1}
		n.Next = n
		_, err := DecodeAtomic("Val = 2", n)
		if err == nil || !strings.Contains(err.Error(), "cyclic") {
			t.Fatalf("err = %v", err)
		}
		if n.Val != 1 || n.Next != n {
			t.Errorf("node changed: %+v", n)
		}
	})
}

func TestDecodeAtomicInvalidTarget(t *testing.T) {
	if _, err := DecodeAtomic("a = 1", "not a pointer"); err == nil {
		t.Error("expected error for non-pointer")
	}
	var nilCfg *atomicConfig
	if _, err := DecodeAtomic("a = 1", nilCfg); err == nil {
		t.Error("expected error for nil pointer")
	}
	var i int
	if _, err := DecodeAtomic("a = 1", &i); err == nil {
		t.Error("expected error for non-struct/map type")
	}
}

func ExampleDecodeAtomic() {
	type Config struct {
		Name string
		Tags []string
		Port int
	}

	// Successful decode: the input replaces the covered values.
	cfg := Config{Name: "old", Tags: []string{"a"}, Port: 1}
	fmt.Println("before:", cfg)
	_, err := DecodeAtomic("Name = \"new\"\nPort = 8080\n", &cfg)
	fmt.Println("after: ", cfg, err)

	// Failed decode: Port has the wrong type, so nothing is applied and
	// the original value is fully preserved.
	cfg = Config{Name: "old", Tags: []string{"a"}, Port: 1}
	fmt.Println("before:", cfg)
	_, err = DecodeAtomic("Name = \"new\"\nPort = \"oops\"\n", &cfg)
	fmt.Println("after: ", cfg, err != nil)

	// Output:
	// before: {old [a] 1}
	// after:  {new [a] 8080} <nil>
	// before: {old [a] 1}
	// after:  {old [a] 1} true
}
