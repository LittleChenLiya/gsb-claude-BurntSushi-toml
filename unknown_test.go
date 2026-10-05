package toml

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type ukTLS struct {
	Cert string `toml:"cert"`
}

type ukServer struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
	TLS  ukTLS  `toml:"tls"`
}

type ukConfig struct {
	Server ukServer `toml:"server"`
	Debug  bool     `toml:"debug"`
}

func TestUnknownKeyRetainAndReject(t *testing.T) {
	data := `
y = "rejected"

[server]
host = "localhost"
x = "retained"
`
	dec := NewDecoder(strings.NewReader(data))
	dec.SetUnknownKeyStrategy(NewUnknownKeyStrategy(UnknownKeyReject).RetainAt("server"))

	var cfg ukConfig
	md, err := dec.Decode(&cfg)

	var uke *UnknownKeyError
	if !errors.As(err, &uke) {
		t.Fatalf("expected *UnknownKeyError, got %T: %v", err, err)
	}
	if len(uke.Keys) != 1 || uke.Keys[0].String() != "y" {
		t.Errorf("rejected keys = %v, want [y]", uke.Keys)
	}

	if cfg.Server.Host != "localhost" {
		t.Errorf("server.host = %q, want %q", cfg.Server.Host, "localhost")
	}

	retained := md.Retained()
	if retained == nil {
		t.Fatal("retained tree is nil; want server.x")
	}
	server, ok := retained["server"].(map[string]any)
	if !ok {
		t.Fatalf("retained[server] = %T, want map[string]any", retained["server"])
	}
	x, ok := server["x"]
	if !ok {
		t.Fatal("retained[server] has no key x")
	}
	if s, ok := x.(string); !ok || s != "retained" {
		t.Errorf("retained[server][x] = %v (%T), want %q (string)", x, x, "retained")
	}
}

func TestUnknownKeyLiteralDot(t *testing.T) {
	data := `"a.b" = 1`
	dec := NewDecoder(strings.NewReader(data))
	dec.SetUnknownKeyStrategy(NewUnknownKeyStrategy(UnknownKeyReject))

	var cfg ukConfig
	_, err := dec.Decode(&cfg)
	var uke *UnknownKeyError
	if !errors.As(err, &uke) {
		t.Fatalf("expected *UnknownKeyError, got %T: %v", err, err)
	}
	if len(uke.Keys) != 1 {
		t.Fatalf("rejected keys = %v, want a single key", uke.Keys)
	}
	if got := uke.Keys[0].String(); got != `"a.b"` {
		t.Errorf("rejected key = %q, want %q", got, `"a.b"`)
	}

	dec2 := NewDecoder(strings.NewReader(data))
	dec2.SetUnknownKeyStrategy(NewUnknownKeyStrategy(UnknownKeyRetain))
	var cfg2 ukConfig
	md2, err := dec2.Decode(&cfg2)
	if err != nil {
		t.Fatalf("retain decode failed: %v", err)
	}
	retained := md2.Retained()
	if retained == nil {
		t.Fatal("retained tree is nil")
	}
	if _, ok := retained["a.b"]; !ok {
		t.Errorf("retained has no key %q; tree = %v", "a.b", retained)
	}
	if _, ok := retained["a"]; ok {
		t.Errorf("retained has unexpected nested key %q; tree = %v", "a", retained)
	}
}

func TestUnknownKeyCustomDecoderConsumed(t *testing.T) {
	data := `
[custom]
foo = 1
bar = 2
`
	dec := NewDecoder(strings.NewReader(data))
	dec.SetUnknownKeyStrategy(NewUnknownKeyStrategy(UnknownKeyReject))

	var cfg struct {
		Custom ukCustom `toml:"custom"`
	}
	md, err := dec.Decode(&cfg)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if und := md.Undecoded(); len(und) != 0 {
		t.Errorf("Undecoded = %v, want empty", und)
	}
	if md.Retained() != nil {
		t.Errorf("Retained = %v, want nil", md.Retained())
	}
}

type ukCustom struct{}

func (ukCustom) UnmarshalTOML(data any) error { return nil }

func TestUnknownKeyStrategyPriority(t *testing.T) {
	data := `
[server]
x = 1

[server.tls]
z = 2
`
	dec := NewDecoder(strings.NewReader(data))
	dec.SetUnknownKeyStrategy(NewUnknownKeyStrategy(UnknownKeyReject).
		RetainAt("server").
		RejectAt("server", "tls"))

	var cfg ukConfig
	md, err := dec.Decode(&cfg)

	retained := md.Retained()
	if retained == nil {
		t.Fatal("retained tree is nil")
	}
	server, ok := retained["server"].(map[string]any)
	if !ok {
		t.Fatalf("retained[server] = %T, want map", retained["server"])
	}
	if _, ok := server["x"]; !ok {
		t.Errorf("retained[server] has no x; tree = %v", retained)
	}
	var uke *UnknownKeyError
	if !errors.As(err, &uke) {
		t.Fatalf("expected *UnknownKeyError, got %T: %v", err, err)
	}
	found := false
	for _, k := range uke.Keys {
		if k.String() == "server.tls.z" {
			found = true
		}
	}
	if !found {
		t.Errorf("rejected keys = %v, want server.tls.z", uke.Keys)
	}
}

func TestUnknownKeyIgnore(t *testing.T) {
	data := `
y = "ignored"
[server]
x = "also ignored"
`
	dec := NewDecoder(strings.NewReader(data))
	dec.SetUnknownKeyStrategy(NewUnknownKeyStrategy(UnknownKeyIgnore))

	var cfg ukConfig
	md, err := dec.Decode(&cfg)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if md.Retained() != nil {
		t.Errorf("Retained = %v, want nil", md.Retained())
	}
}

func TestUnknownKeyNoStrategy(t *testing.T) {
	data := `
y = "ignored"
[server]
x = "also ignored"
`
	var cfg ukConfig
	md, err := Decode(data, &cfg)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if md.Retained() != nil {
		t.Errorf("Retained = %v, want nil", md.Retained())
	}
}

func TestUnknownKeyRejectAll(t *testing.T) {
	data := `
a = 1
b = 2
c = 3
`
	dec := NewDecoder(strings.NewReader(data))
	dec.SetUnknownKeyStrategy(NewUnknownKeyStrategy(UnknownKeyReject))

	var cfg ukConfig
	_, err := dec.Decode(&cfg)
	var uke *UnknownKeyError
	if !errors.As(err, &uke) {
		t.Fatalf("expected *UnknownKeyError, got %T: %v", err, err)
	}
	want := map[string]bool{"a": true, "b": true, "c": true}
	if len(uke.Keys) != len(want) {
		t.Fatalf("rejected keys = %v, want %d keys", uke.Keys, len(want))
	}
	for _, k := range uke.Keys {
		if !want[k.String()] {
			t.Errorf("unexpected rejected key %q", k.String())
		}
	}
}

func TestUnknownKeyRetainTypes(t *testing.T) {
	data := `
str = "hello"
int = 42
float = 3.14
bool = true
array = [1, 2, 3]
inline = {a = 1, b = "two"}
dt = 1979-05-27T07:32:00Z
`
	dec := NewDecoder(strings.NewReader(data))
	dec.SetUnknownKeyStrategy(NewUnknownKeyStrategy(UnknownKeyRetain))

	var cfg ukConfig
	md, err := dec.Decode(&cfg)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	retained := md.Retained()
	if retained == nil {
		t.Fatal("retained tree is nil")
	}

	check := func(key string, want any) {
		t.Helper()
		got, ok := retained[key]
		if !ok {
			t.Errorf("retained has no key %q", key)
			return
		}
		if reflect.TypeOf(got) != reflect.TypeOf(want) {
			t.Errorf("retained[%q] type = %T, want %T", key, got, want)
		}
	}

	check("str", "")
	check("int", int64(0))
	check("float", float64(0))
	check("bool", false)
	check("array", []any{})
	check("inline", map[string]any{})
	check("dt", time.Time{})

	if retained["str"] != "hello" {
		t.Errorf("str = %v, want hello", retained["str"])
	}
	if retained["int"] != int64(42) {
		t.Errorf("int = %v, want 42", retained["int"])
	}
	if retained["bool"] != true {
		t.Errorf("bool = %v, want true", retained["bool"])
	}
}

func TestUnknownKeyNestedTables(t *testing.T) {
	data := `
[server.tls]
cert = "x"
extra = "retained"
`
	dec := NewDecoder(strings.NewReader(data))
	dec.SetUnknownKeyStrategy(NewUnknownKeyStrategy(UnknownKeyReject).RetainAt("server"))

	var cfg ukConfig
	md, err := dec.Decode(&cfg)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	// cert is a known field and is decoded; extra is unknown and retained.
	if cfg.Server.TLS.Cert != "x" {
		t.Errorf("server.tls.cert = %q, want x", cfg.Server.TLS.Cert)
	}
	retained := md.Retained()
	if retained == nil {
		t.Fatal("retained tree is nil")
	}
	server, ok := retained["server"].(map[string]any)
	if !ok {
		t.Fatalf("retained[server] = %T, want map", retained["server"])
	}
	tls, ok := server["tls"].(map[string]any)
	if !ok {
		t.Fatalf("retained[server][tls] = %T, want map", server["tls"])
	}
	if tls["extra"] != "retained" {
		t.Errorf("retained[server][tls][extra] = %v, want retained", tls["extra"])
	}
}

func TestUnknownKeyRejectDoesNotDeliver(t *testing.T) {
	data := `
y = "rejected"
[server]
host = "localhost"
`
	dec := NewDecoder(strings.NewReader(data))
	dec.SetUnknownKeyStrategy(NewUnknownKeyStrategy(UnknownKeyReject))

	var cfg ukConfig
	_, err := dec.Decode(&cfg)
	if err == nil {
		t.Fatal("expected error for rejected key, got nil")
	}
	var uke *UnknownKeyError
	if !errors.As(err, &uke) {
		t.Fatalf("expected *UnknownKeyError, got %T: %v", err, err)
	}
}

func TestUnknownKeyPolicyString(t *testing.T) {
	tests := []struct {
		p    UnknownKeyPolicy
		want string
	}{
		{UnknownKeyIgnore, "ignore"},
		{UnknownKeyReject, "reject"},
		{UnknownKeyRetain, "retain"},
	}
	for _, tt := range tests {
		if got := tt.p.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", tt.p, got, tt.want)
		}
	}
}
