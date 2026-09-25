package toml

import (
	"errors"
	"strings"
	"testing"
)

func TestKeyMigration(t *testing.T) {
	var s struct {
		Server struct {
			ReadTimeout int `toml:"read_timeout"`
		}
	}
	md, err := DecodeWithMigrations("\n[server]\ntimeout = 30\n", &s,
		KeyMigration{From: Key{"server", "timeout"}, To: Key{"server", "read_timeout"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Server.ReadTimeout != 30 {
		t.Fatalf("expected ReadTimeout 30, got %d", s.Server.ReadTimeout)
	}

	if len(md.Migrations) != 1 {
		t.Fatalf("expected 1 migration record, got %d", len(md.Migrations))
	}
	rec := md.Migrations[0]
	if rec.From.String() != "server.timeout" || rec.To.String() != "server.read_timeout" {
		t.Fatalf("unexpected record paths: %q -> %q", rec.From, rec.To)
	}
	if rec.Field != "read_timeout" {
		t.Fatalf("unexpected record field: %q", rec.Field)
	}
	if rec.Position.Line != 3 {
		t.Fatalf("expected source line 3, got %d", rec.Position.Line)
	}

	if !md.IsDefined("server", "read_timeout") {
		t.Fatal("expected server.read_timeout to be defined after migration")
	}
	if md.IsDefined("server", "timeout") {
		t.Fatal("expected server.timeout to be gone after migration")
	}
	if undecoded := md.Undecoded(); len(undecoded) != 0 {
		t.Fatalf("expected no undecoded keys, got %v", undecoded)
	}
}

func TestKeyMigrationConflictBothKeys(t *testing.T) {
	var s struct {
		Server struct {
			ReadTimeout int `toml:"read_timeout"`
		}
	}
	input := "\n[server]\ntimeout = 30\nread_timeout = 99\n"
	_, err := DecodeWithMigrations(input, &s,
		KeyMigration{From: Key{"server", "timeout"}, To: Key{"server", "read_timeout"}})
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected 'already exists' conflict, got: %v", err)
	}
	var perr ParseError
	if !errors.As(err, &perr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if perr.Position.Line != 3 {
		t.Fatalf("expected conflict reported at source line 3, got %d", perr.Position.Line)
	}
	// No silent overwrite: the decode failed as a whole and the target
	// value was not written.
	if s.Server.ReadTimeout != 0 {
		t.Fatalf("expected no value written, got %d", s.Server.ReadTimeout)
	}
}

func TestKeyMigrationChainAndCycle(t *testing.T) {
	var v map[string]any

	chain := []KeyMigration{
		{From: Key{"a"}, To: Key{"b"}},
		{From: Key{"b"}, To: Key{"c"}},
	}
	if _, err := DecodeWithMigrations("a = 1\n", &v, chain...); err == nil ||
		!strings.Contains(err.Error(), "chains and cycles") {
		t.Fatalf("expected chain error, got: %v", err)
	}

	cycle := []KeyMigration{
		{From: Key{"a"}, To: Key{"b"}},
		{From: Key{"b"}, To: Key{"a"}},
	}
	if _, err := DecodeWithMigrations("a = 1\n", &v, cycle...); err == nil ||
		!strings.Contains(err.Error(), "chains and cycles") {
		t.Fatalf("expected cycle error, got: %v", err)
	}
}

func TestKeyMigrationIntoScalar(t *testing.T) {
	var v map[string]any
	_, err := DecodeWithMigrations("[server]\nport = 1\ntimeout = 30\n", &v,
		KeyMigration{From: Key{"server", "timeout"}, To: Key{"server", "port", "nested"}})
	if err == nil || !strings.Contains(err.Error(), "not a table") {
		t.Fatalf("expected 'not a table' conflict, got: %v", err)
	}
}

func TestKeyMigrationArrayOfTables(t *testing.T) {
	var s struct {
		Servers []struct {
			ReadTimeout int `toml:"read_timeout"`
		} `toml:"servers"`
	}
	md, err := DecodeWithMigrations("\n[[servers]]\ntimeout = 1\n\n[[servers]]\ntimeout = 2\n", &s,
		KeyMigration{
			From: Key{"servers", Wildcard, "timeout"},
			To:   Key{"servers", Wildcard, "read_timeout"},
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Servers) != 2 || s.Servers[0].ReadTimeout != 1 || s.Servers[1].ReadTimeout != 2 {
		t.Fatalf("unexpected decode result: %+v", s.Servers)
	}
	if len(md.Migrations) != 2 {
		t.Fatalf("expected 2 migration records, got %d", len(md.Migrations))
	}
	for i, rec := range md.Migrations {
		if rec.Field != "read_timeout" || rec.Position.Line == 0 {
			t.Fatalf("record %d: unexpected field %q or position %+v", i, rec.Field, rec.Position)
		}
	}
}

func TestKeyMigrationArrayOfTablesConflict(t *testing.T) {
	var s struct {
		Servers []struct {
			Timeout     int `toml:"timeout"`
			ReadTimeout int `toml:"read_timeout"`
		} `toml:"servers"`
	}
	_, err := DecodeWithMigrations("\n[[servers]]\ntimeout = 1\n\n[[servers]]\ntimeout = 2\nread_timeout = 3\n", &s,
		KeyMigration{
			From: Key{"servers", Wildcard, "timeout"},
			To:   Key{"servers", Wildcard, "read_timeout"},
		})
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	// The conflict is in the second element; it must be identified as such.
	if !strings.Contains(err.Error(), "servers[1]") {
		t.Fatalf("expected error to identify second element servers[1], got: %v", err)
	}
}

func TestKeyMigrationQuotedDotKey(t *testing.T) {
	var s struct {
		Server struct {
			ReadTimeout int `toml:"read_timeout"`
		}
	}
	// "server.timeout" is a single literal quoted key, not a nested path.
	md, err := DecodeWithMigrations("\"server.timeout\" = 30\n", &s,
		KeyMigration{From: Key{"server.timeout"}, To: Key{"server", "read_timeout"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Server.ReadTimeout != 30 {
		t.Fatalf("expected ReadTimeout 30, got %d", s.Server.ReadTimeout)
	}
	if len(md.Migrations) != 1 || md.Migrations[0].From.String() != "\"server.timeout\"" {
		t.Fatalf("unexpected records: %+v", md.Migrations)
	}

	// A nested path must not match the literal dotted key.
	var v map[string]any
	md, err = DecodeWithMigrations("\"server.timeout\" = 30\n", &v,
		KeyMigration{From: Key{"server", "timeout"}, To: Key{"server", "read_timeout"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(md.Migrations) != 0 {
		t.Fatalf("nested path must not match literal dotted key: %+v", md.Migrations)
	}
}

func TestKeyMigrationUnknownTargetField(t *testing.T) {
	var s struct {
		Server struct{}
	}
	md, err := DecodeWithMigrations("[server]\ntimeout = 30\n", &s,
		KeyMigration{From: Key{"server", "timeout"}, To: Key{"server", "nonexistent"}})
	if err != nil {
		t.Fatal(err)
	}
	undecoded := md.Undecoded()
	if len(undecoded) != 1 || undecoded[0].String() != "server.nonexistent" {
		t.Fatalf("expected server.nonexistent undecoded, got %v", undecoded)
	}
}

func TestKeyMigrationEmptyTable(t *testing.T) {
	var s struct {
		Server struct {
			ReadTimeout int `toml:"read_timeout"`
		}
	}
	md, err := DecodeWithMigrations("[server]\n", &s,
		KeyMigration{From: Key{"server", "timeout"}, To: Key{"server", "read_timeout"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(md.Migrations) != 0 {
		t.Fatalf("expected no migrations on empty table, got %+v", md.Migrations)
	}
}

func TestKeyMigrationIntOverflow(t *testing.T) {
	var s struct {
		Server struct {
			ReadTimeout int8 `toml:"read_timeout"`
		}
	}
	_, err := DecodeWithMigrations("\n[server]\ntimeout = 300\n", &s,
		KeyMigration{From: Key{"server", "timeout"}, To: Key{"server", "read_timeout"}})
	if err == nil {
		t.Fatal("expected integer overflow error, got nil")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("expected range error, got: %v", err)
	}
	var perr ParseError
	if !errors.As(err, &perr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if perr.Position.Line != 3 {
		t.Fatalf("expected error at original source line 3, got %d", perr.Position.Line)
	}
}

func TestDecodeDoesNotMigrate(t *testing.T) {
	var s struct {
		Server struct {
			ReadTimeout int `toml:"read_timeout"`
		}
	}
	md, err := Decode("[server]\ntimeout = 30\n", &s)
	if err != nil {
		t.Fatal(err)
	}
	if s.Server.ReadTimeout != 0 {
		t.Fatal("plain Decode must not apply migrations")
	}
	if len(md.Migrations) != 0 {
		t.Fatalf("plain Decode must not record migrations, got %+v", md.Migrations)
	}
	if undecoded := md.Undecoded(); len(undecoded) != 1 || undecoded[0].String() != "server.timeout" {
		t.Fatalf("expected server.timeout undecoded, got %v", undecoded)
	}
}