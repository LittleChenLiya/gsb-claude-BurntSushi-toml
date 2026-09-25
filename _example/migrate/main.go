// Command migrate demonstrates explicit key migration decoding with
// toml.DecodeWithMigrations: it migrates the deprecated "server.timeout"
// key to "server.read_timeout" before decoding, reports the source position
// of every applied migration, and shows how conflicts (both old and new key
// present) are reported with their location instead of silently overwriting.
//
// Run with:
//
//	go run ./_example/migrate
package main

import (
	"errors"
	"fmt"

	"github.com/BurntSushi/toml"
)

type config struct {
	Server struct {
		ReadTimeout int `toml:"read_timeout"`
	} `toml:"server"`
}

var migration = toml.KeyMigration{
	From: toml.Key{"server", "timeout"},
	To:   toml.Key{"server", "read_timeout"},
}

func main() {
	// 1. Successful migration: the old key is moved and decoded, and the
	//    result records the source position and target field.
	ok := "[server]\ntimeout = 30\n"
	var c config
	md, err := toml.DecodeWithMigrations(ok, &c, migration)
	if err != nil {
		fmt.Println("unexpected error:", err)
		return
	}
	fmt.Printf("success: server.read_timeout = %d\n", c.Server.ReadTimeout)
	for _, m := range md.Migrations {
		fmt.Printf("migrated: %s -> %s (field %q, defined at line %d, column %d)\n",
			m.From, m.To, m.Field, m.Position.Line, m.Position.Col)
	}

	// 2. Conflict: both the old and new key exist. This is an error with the
	//    conflict location, never a silent overwrite.
	conflict := "[server]\ntimeout = 30\nread_timeout = 99\n"
	_, err = toml.DecodeWithMigrations(conflict, &c, migration)
	fmt.Println()
	fmt.Println("conflict:")
	var perr toml.ParseError
	if errors.As(err, &perr) {
		fmt.Println(perr.ErrorWithPosition())
	} else {
		fmt.Println(err)
	}
}
