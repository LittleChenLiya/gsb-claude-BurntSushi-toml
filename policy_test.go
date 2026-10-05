package toml

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type policyServerConfig struct {
	Name   string `toml:"name"`
	Server struct {
		Host string `toml:"host"`
	} `toml:"server"`
}

const policyDoc = `
name = "app"
y    = 1

[server]
host = "localhost"
x    = 2020-01-02T03:04:05Z
`

func decodeWithPolicy(t *testing.T, doc string, p DecodePolicy, v any) (MetaData, error) {
	t.Helper()
	return NewDecoder(strings.NewReader(doc)).Policy(p).Decode(v)
}

// Unknown key "x" inside [server] is kept in the sidecar tree; unknown
// top-level key "y" is rejected. Both diagnostics must be accurate, and the
// target object must not be delivered.
func TestPolicyKeepInServerRejectTopLevel(t *testing.T) {
	var conf policyServerConfig
	md, err := decodeWithPolicy(t, policyDoc, DecodePolicy{
		Default: RejectUnknown,
		Rules: []PolicyRule{
			{Path: Key{"server"}, Action: KeepUnknown},
		},
	}, &conf)

	var ukErr UnknownKeyError
	if !errors.As(err, &ukErr) {
		t.Fatalf("expected UnknownKeyError, got %v (%T)", err, err)
	}
	if want := []Key{{"y"}}; !reflect.DeepEqual(ukErr.Keys, want) {
		t.Errorf("rejected keys: have %q, want %q", ukErr.Keys, want)
	}

	// Target object must not be delivered on rejection.
	if conf.Name != "" || conf.Server.Host != "" {
		t.Errorf("target was delivered despite rejection: %+v", conf)
	}

	// The kept sidecar tree holds server.x with its TOML type preserved.
	x, ok := lookupKey(md.Kept(), Key{"server", "x"})
	if !ok {
		t.Fatalf("server.x not in kept tree: %#v", md.Kept())
	}
	dt, ok := x.(time.Time)
	if !ok {
		t.Fatalf("kept server.x has type %T, want time.Time", x)
	}
	if want := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC); !dt.Equal(want) {
		t.Errorf("kept server.x = %v, want %v", dt, want)
	}
}

// Without the rejected key the decode succeeds, the kept value is available,
// and kept keys no longer show up as undecoded.
func TestPolicyKeepOnly(t *testing.T) {
	doc := `
name = "app"

[server]
host   = "localhost"
x      = 42
ratio  = 1.5
tags   = ["a", "b"]
verify = true

[server.limits]
cpu = 4
`
	var conf policyServerConfig
	md, err := decodeWithPolicy(t, doc, DecodePolicy{
		Rules: []PolicyRule{
			{Path: Key{"server"}, Action: KeepUnknown},
		},
	}, &conf)
	if err != nil {
		t.Fatal(err)
	}
	if conf.Name != "app" || conf.Server.Host != "localhost" {
		t.Errorf("decoded config wrong: %+v", conf)
	}

	// "limits" is an entirely unknown table: kept as a whole subtree.
	want := map[string]any{
		"server": map[string]any{
			"x":      int64(42),
			"ratio":  1.5,
			"tags":   []any{"a", "b"},
			"verify": true,
			"limits": map[string]any{"cpu": int64(4)},
		},
	}
	if !reflect.DeepEqual(md.Kept(), want) {
		t.Errorf("kept tree:\nhave %#v\nwant %#v", md.Kept(), want)
	}

	// Kept keys are handled and no longer undecoded.
	for _, k := range md.Undecoded() {
		t.Errorf("kept key %q still listed as undecoded", k)
	}
}

// All unknown paths are reported in document order; an unknown table is
// reported by its own path only.
func TestPolicyRejectListsAllUnknownPaths(t *testing.T) {
	doc := `
a = 1
z = 2

[t]
c = 3
`
	var conf struct{}
	_, err := decodeWithPolicy(t, doc, DecodePolicy{Default: RejectUnknown}, &conf)

	var ukErr UnknownKeyError
	if !errors.As(err, &ukErr) {
		t.Fatalf("expected UnknownKeyError, got %v", err)
	}
	want := []Key{{"a"}, {"z"}, {"t"}}
	if !reflect.DeepEqual(ukErr.Keys, want) {
		t.Errorf("rejected keys: have %q, want %q", ukErr.Keys, want)
	}
	for _, k := range want {
		if !strings.Contains(err.Error(), k.String()) {
			t.Errorf("error message %q does not mention %q", err, k)
		}
	}
}

// The zero policy keeps the historical behaviour: unknown keys are ignored
// and only visible through Undecoded.
func TestPolicyZeroValueIgnores(t *testing.T) {
	var conf policyServerConfig
	md, err := decodeWithPolicy(t, policyDoc, DecodePolicy{}, &conf)
	if err != nil {
		t.Fatal(err)
	}
	if conf.Name != "app" || conf.Server.Host != "localhost" {
		t.Errorf("decoded config wrong: %+v", conf)
	}
	if md.Kept() != nil {
		t.Errorf("kept tree should be nil, got %#v", md.Kept())
	}
	var found []string
	for _, k := range md.Undecoded() {
		found = append(found, k.String())
	}
	want := []string{"y", "server.x"}
	if !reflect.DeepEqual(found, want) {
		t.Errorf("undecoded: have %q, want %q", found, want)
	}
}

// A dot inside a quoted key name is a literal part of the name and must not
// be split into path elements.
func TestPolicyLiteralDotInKeyName(t *testing.T) {
	doc := `
"a.b" = 1

[a]
b = 2
`
	// Rule for the two-element path a.b must reject the key b inside [a],
	// but not the literal "a.b" key.
	var conf struct{}
	_, err := decodeWithPolicy(t, doc, DecodePolicy{
		Rules: []PolicyRule{
			{Path: Key{"a", "b"}, Action: RejectUnknown},
		},
	}, &conf)
	var ukErr UnknownKeyError
	if !errors.As(err, &ukErr) {
		t.Fatalf("expected UnknownKeyError, got %v", err)
	}
	if want := []Key{{"a", "b"}}; !reflect.DeepEqual(ukErr.Keys, want) {
		t.Errorf("rejected keys: have %q, want %q", ukErr.Keys, want)
	}

	// Rule parsed from the quoted form matches the literal key exactly.
	path, err := ParseKeyPath(`"a.b"`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = decodeWithPolicy(t, doc, DecodePolicy{
		Rules: []PolicyRule{
			{Path: path, Action: RejectUnknown},
		},
	}, &conf)
	ukErr = UnknownKeyError{}
	if !errors.As(err, &ukErr) {
		t.Fatalf("expected UnknownKeyError, got %v", err)
	}
	if want := []Key{{"a.b"}}; !reflect.DeepEqual(ukErr.Keys, want) {
		t.Errorf("rejected keys: have %q, want %q", ukErr.Keys, want)
	}
}

// Keys consumed by a custom Unmarshaler are never reported as unknown.
func TestPolicyCustomUnmarshalerConsumesKeys(t *testing.T) {
	var conf struct {
		Custom policyCustom `toml:"custom"`
	}
	doc := `
[custom]
a = 1
b = "two"
`
	_, err := decodeWithPolicy(t, doc, DecodePolicy{Default: RejectUnknown}, &conf)
	if err != nil {
		t.Fatalf("keys consumed by UnmarshalTOML reported as unknown: %v", err)
	}
	if conf.Custom.seen["a"] != int64(1) || conf.Custom.seen["b"] != "two" {
		t.Errorf("custom decoder saw %#v", conf.Custom.seen)
	}
}

type policyCustom struct{ seen map[string]any }

func (c *policyCustom) UnmarshalTOML(v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("expected table, got %T", v)
	}
	c.seen = m
	return nil
}

// Field aliases from toml tags and nested tables follow the original key
// mapping: the TOML key name is what the policy sees.
func TestPolicyFieldAliasAndNestedTable(t *testing.T) {
	var conf struct {
		Renamed string `toml:"renamed"`
		Nested  struct {
			Val int `toml:"val"`
		} `toml:"nested"`
	}
	doc := `
renamed = "ok"

[nested]
val   = 1
extra = 2
`
	md, err := decodeWithPolicy(t, doc, DecodePolicy{
		Rules: []PolicyRule{
			{Path: Key{"nested", "extra"}, Action: KeepUnknown},
		},
	}, &conf)
	if err != nil {
		t.Fatal(err)
	}
	if conf.Renamed != "ok" || conf.Nested.Val != 1 {
		t.Errorf("decoded config wrong: %+v", conf)
	}
	v, ok := lookupKey(md.Kept(), Key{"nested", "extra"})
	if !ok || v != int64(2) {
		t.Errorf("kept tree missing nested.extra: %#v", md.Kept())
	}
}

// Rule priority: the most specific (longest) matching path wins; on equal
// path length the later rule wins; otherwise the default applies.
func TestPolicyPriority(t *testing.T) {
	doc := `
[server]
x = 1
y = 2
`
	// server.x rejects (longer path) even though server keeps.
	var conf struct{}
	_, err := decodeWithPolicy(t, doc, DecodePolicy{
		Default: IgnoreUnknown,
		Rules: []PolicyRule{
			{Path: Key{"server"}, Action: KeepUnknown},
			{Path: Key{"server", "x"}, Action: RejectUnknown},
		},
	}, &conf)
	var ukErr UnknownKeyError
	if !errors.As(err, &ukErr) {
		t.Fatalf("expected UnknownKeyError, got %v", err)
	}
	if want := []Key{{"server", "x"}}; !reflect.DeepEqual(ukErr.Keys, want) {
		t.Errorf("rejected keys: have %q, want %q", ukErr.Keys, want)
	}

	// On equal path length the later rule wins.
	_, err = decodeWithPolicy(t, doc, DecodePolicy{
		Rules: []PolicyRule{
			{Path: Key{"server"}, Action: RejectUnknown},
			{Path: Key{"server"}, Action: KeepUnknown},
		},
	}, &conf)
	if err != nil {
		t.Fatalf("later rule should win and keep: %v", err)
	}
}

func TestParseKeyPath(t *testing.T) {
	tests := []struct {
		in   string
		want Key
	}{
		{"a", Key{"a"}},
		{"a.b", Key{"a", "b"}},
		{"a. b .c", Key{"a", "b", "c"}},
		{`"a.b"`, Key{"a.b"}},
		{`'a.b'`, Key{"a.b"}},
		{`server."a.b".c`, Key{"server", "a.b", "c"}},
		{`"a\"b".c`, Key{`a"b`, "c"}},
	}
	for _, tt := range tests {
		have, err := ParseKeyPath(tt.in)
		if err != nil {
			t.Errorf("ParseKeyPath(%q): %v", tt.in, err)
			continue
		}
		if !reflect.DeepEqual(have, tt.want) {
			t.Errorf("ParseKeyPath(%q) = %q, want %q", tt.in, have, tt.want)
		}
	}

	for _, bad := range []string{"", "a..b", "a.", ".a", `a."b`, "a b"} {
		if _, err := ParseKeyPath(bad); err == nil {
			t.Errorf("ParseKeyPath(%q): expected error", bad)
		}
	}
}
