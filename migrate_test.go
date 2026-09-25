package toml

import (
	"errors"
	"strings"
	"testing"
)

type migServer struct {
	ReadTimeout int `toml:"read_timeout"`
}

type migConfig struct {
	Server migServer `toml:"server"`
}

var migTimeout = KeyMigration{
	From: Key{"server", "timeout"},
	To:   Key{"server", "read_timeout"},
}

func TestKeyMigration(t *testing.T) {
	var cfg migConfig
	_, applied, err := NewDecoder(strings.NewReader(
		"[server]\ntimeout = 30\n")).DecodeWithKeyMigrations(&cfg, migTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ReadTimeout != 30 {
		t.Fatalf("expected read_timeout 30, got %d", cfg.Server.ReadTimeout)
	}
	if len(applied) != 1 {
		t.Fatalf("expected 1 applied migration, got %d", len(applied))
	}
	m := applied[0]
	if m.From.String() != "server.timeout" || m.To.String() != "server.read_timeout" {
		t.Fatalf("unexpected migration %q -> %q", m.From, m.To)
	}
	if m.Position.Line != 2 {
		t.Fatalf("expected source line 2, got %d", m.Position.Line)
	}
	if m.Index != -1 {
		t.Fatalf("expected index -1, got %d", m.Index)
	}
	if m.Field != "read_timeout" {
		t.Fatalf("expected target field read_timeout, got %q", m.Field)
	}
}

func TestKeyMigrationConflict(t *testing.T) {
	var cfg migConfig
	in := "[server]\ntimeout = 30\nread_timeout = 60\n"
	_, _, err := NewDecoder(strings.NewReader(in)).DecodeWithKeyMigrations(&cfg, migTimeout)
	if err == nil {
		t.Fatal("expected a conflict error, got none")
	}
	if !strings.Contains(err.Error(), "both keys exist") {
		t.Fatalf("expected a 'both keys exist' error, got: %v", err)
	}
	var perr ParseError
	if !errors.As(err, &perr) {
		t.Fatalf("expected a ParseError, got %T", err)
	}
	if perr.Position.Line != 3 {
		t.Fatalf("expected conflict at line 3, got line %d", perr.Position.Line)
	}
	// No silent overwrite: the value must not have been decoded.
	if cfg.Server.ReadTimeout != 0 {
		t.Fatalf("value was silently overwritten: %d", cfg.Server.ReadTimeout)
	}
}

func TestKeyMigrationArraySecondElement(t *testing.T) {
	type servers struct {
		Servers []migServer `toml:"servers"`
	}
	in := strings.Join([]string{
		"[[servers]]", // line 1
		"timeout = 1", // line 2
		"",
		"[[servers]]",      // line 4
		"timeout = 2",      // line 5
		"read_timeout = 3", // line 6
	}, "\n")
	mig := KeyMigration{
		From: Key{"servers", "*", "timeout"},
		To:   Key{"servers", "*", "read_timeout"},
	}
	var cfg servers
	_, _, err := NewDecoder(strings.NewReader(in)).DecodeWithKeyMigrations(&cfg, mig)
	if err == nil {
		t.Fatal("expected a conflict error, got none")
	}
	if !strings.Contains(err.Error(), "element 2") {
		t.Fatalf("expected the error to point out element 2, got: %v", err)
	}
	var perr ParseError
	if !errors.As(err, &perr) {
		t.Fatalf("expected a ParseError, got %T", err)
	}
	if perr.Position.Line != 6 {
		t.Fatalf("expected conflict at line 6, got line %d", perr.Position.Line)
	}
}

func TestKeyMigrationWildcard(t *testing.T) {
	type servers struct {
		Servers []migServer `toml:"servers"`
	}
	in := strings.Join([]string{
		"[[servers]]", // line 1
		"timeout = 1", // line 2
		"[[servers]]", // line 3
		"timeout = 2", // line 4
	}, "\n")
	mig := KeyMigration{
		From: Key{"servers", "*", "timeout"},
		To:   Key{"servers", "*", "read_timeout"},
	}
	var cfg servers
	_, applied, err := NewDecoder(strings.NewReader(in)).DecodeWithKeyMigrations(&cfg, mig)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 2 || cfg.Servers[0].ReadTimeout != 1 || cfg.Servers[1].ReadTimeout != 2 {
		t.Fatalf("unexpected decode result: %+v", cfg.Servers)
	}
	if len(applied) != 2 {
		t.Fatalf("expected 2 applied migrations, got %d", len(applied))
	}
	if applied[0].Index != 0 || applied[0].Position.Line != 2 {
		t.Fatalf("unexpected first migration: %+v", applied[0])
	}
	if applied[1].Index != 1 || applied[1].Position.Line != 4 {
		t.Fatalf("unexpected second migration: %+v", applied[1])
	}
	if applied[1].Field != "read_timeout" {
		t.Fatalf("unexpected target field: %q", applied[1].Field)
	}
}

func TestKeyMigrationQuotedKey(t *testing.T) {
	// "server.timeout" is a single literal key, not the nested path
	// server.timeout; the two must not be confused.
	type nested struct {
		Server migServer `toml:"server"`
	}
	in := `"server.timeout" = 30
[server]
timeout = 10
`
	var cfg nested
	_, applied, err := NewDecoder(strings.NewReader(in)).DecodeWithKeyMigrations(&cfg, migTimeout)
	if err != nil {
		t.Fatal(err)
	}
	// Only the nested server.timeout migrated; the literal "server.timeout"
	// key is untouched and undecoded.
	if cfg.Server.ReadTimeout != 10 {
		t.Fatalf("expected read_timeout 10, got %d", cfg.Server.ReadTimeout)
	}
	if len(applied) != 1 || applied[0].From.String() != "server.timeout" {
		t.Fatalf("unexpected applied migrations: %+v", applied)
	}

	// Migrating the literal dotted key works with a single-segment path.
	type flat struct {
		Timeout int `toml:"server_timeout"`
	}
	var fcfg flat
	_, applied, err = NewDecoder(strings.NewReader(`"server.timeout" = 30`)).
		DecodeWithKeyMigrations(&fcfg, KeyMigration{
			From: Key{"server.timeout"},
			To:   Key{"server_timeout"},
		})
	if err != nil {
		t.Fatal(err)
	}
	if fcfg.Timeout != 30 {
		t.Fatalf("expected 30, got %d", fcfg.Timeout)
	}
	if len(applied) != 1 || applied[0].From.String() != `"server.timeout"` {
		t.Fatalf("unexpected applied migrations: %+v", applied)
	}
}

func TestKeyMigrationUnknownField(t *testing.T) {
	var cfg migConfig
	_, _, err := NewDecoder(strings.NewReader("[server]\ntimeout = 30\n")).
		DecodeWithKeyMigrations(&cfg, KeyMigration{
			From: Key{"server", "timeout"},
			To:   Key{"server", "nosuchfield"},
		})
	if err == nil {
		t.Fatal("expected an unknown-field error, got none")
	}
	if !strings.Contains(err.Error(), "does not match any field") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestKeyMigrationEmptyTable(t *testing.T) {
	var cfg migConfig
	_, applied, err := NewDecoder(strings.NewReader("[server]\n")).
		DecodeWithKeyMigrations(&cfg, migTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 {
		t.Fatalf("expected no applied migrations, got %+v", applied)
	}
	if cfg.Server.ReadTimeout != 0 {
		t.Fatalf("expected 0, got %d", cfg.Server.ReadTimeout)
	}
}

func TestKeyMigrationIntOverflow(t *testing.T) {
	type small struct {
		ReadTimeout int8 `toml:"read_timeout"`
	}
	type cfg struct {
		Server small `toml:"server"`
	}
	var c cfg
	_, _, err := NewDecoder(strings.NewReader("[server]\ntimeout = 300\n")).
		DecodeWithKeyMigrations(&c, migTimeout)
	if err == nil {
		t.Fatal("expected an integer overflow error, got none")
	}
	var perr ParseError
	if !errors.As(err, &perr) {
		t.Fatalf("expected a ParseError, got %T", err)
	}
	// The error points at the migrated source key.
	if perr.Position.Line != 2 {
		t.Fatalf("expected overflow at line 2, got line %d", perr.Position.Line)
	}
}

func TestKeyMigrationIntoScalar(t *testing.T) {
	var cfg migConfig
	_, _, err := NewDecoder(strings.NewReader("[server]\ntimeout = 30\nread_timeout = 1\n")).
		DecodeWithKeyMigrations(&cfg, KeyMigration{
			From: Key{"server", "timeout"},
			To:   Key{"server", "read_timeout", "sub"},
		})
	if err == nil {
		t.Fatal("expected a conflict error, got none")
	}
	if !strings.Contains(err.Error(), "not a table") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestKeyMigrationChainAndCycle(t *testing.T) {
	var cfg migConfig
	chain := []KeyMigration{
		{From: Key{"a"}, To: Key{"b"}},
		{From: Key{"b"}, To: Key{"c"}},
	}
	if _, _, err := NewDecoder(strings.NewReader("a = 1\n")).DecodeWithKeyMigrations(&cfg, chain...); err == nil ||
		!strings.Contains(err.Error(), "chained and cyclic") {
		t.Fatalf("expected a chain error, got: %v", err)
	}
	cycle := []KeyMigration{
		{From: Key{"a"}, To: Key{"b"}},
		{From: Key{"b"}, To: Key{"a"}},
	}
	if _, _, err := NewDecoder(strings.NewReader("a = 1\n")).DecodeWithKeyMigrations(&cfg, cycle...); err == nil ||
		!strings.Contains(err.Error(), "chained and cyclic") {
		t.Fatalf("expected a cycle error, got: %v", err)
	}
	dup := []KeyMigration{
		{From: Key{"a"}, To: Key{"b"}},
		{From: Key{"a"}, To: Key{"c"}},
	}
	if _, _, err := NewDecoder(strings.NewReader("a = 1\n")).DecodeWithKeyMigrations(&cfg, dup...); err == nil ||
		!strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected a duplicate error, got: %v", err)
	}
}

func TestKeyMigrationInputUnchanged(t *testing.T) {
	in := "[server]\ntimeout = 30\n"
	var cfg migConfig
	if _, _, err := NewDecoder(strings.NewReader(in)).DecodeWithKeyMigrations(&cfg, migTimeout); err != nil {
		t.Fatal(err)
	}
	if in != "[server]\ntimeout = 30\n" {
		t.Fatalf("input was modified: %q", in)
	}
	// The old key is gone from the decoded metadata, the new key is known.
	md, _, err := NewDecoder(strings.NewReader(in)).DecodeWithKeyMigrations(&cfg, migTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if md.IsDefined("server", "timeout") {
		t.Fatal("old key still defined after migration")
	}
	if !md.IsDefined("server", "read_timeout") {
		t.Fatal("new key not defined after migration")
	}
}

func TestDecodeDoesNotMigrate(t *testing.T) {
	// The regular Decode entry points never apply key migrations.
	var cfg migConfig
	md, err := Decode("[server]\ntimeout = 30\n", &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ReadTimeout != 0 {
		t.Fatalf("Decode applied a migration: %d", cfg.Server.ReadTimeout)
	}
	if !md.IsDefined("server", "timeout") {
		t.Fatal("old key not defined without migration")
	}
	if len(md.Undecoded()) != 1 || md.Undecoded()[0].String() != "server.timeout" {
		t.Fatalf("expected server.timeout to be undecoded, got %v", md.Undecoded())
	}
}
