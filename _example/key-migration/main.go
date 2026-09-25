// Demo of key migrations: run with
//
//	go run ./_example/key-migration
//
// It shows a successful migration (including the source line of the migrated
// key) and two conflict cases reported with their positions.
package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server struct {
		ReadTimeout int `toml:"read_timeout"`
	} `toml:"server"`
}

type Fleet struct {
	Servers []struct {
		ReadTimeout int `toml:"read_timeout"`
	} `toml:"servers"`
}

func main() {
	// 1. A renamed key is migrated before decoding.
	var conf Config
	_, applied, err := toml.NewDecoder(strings.NewReader(
		"[server]\ntimeout = 30\n")).DecodeWithKeyMigrations(&conf, toml.KeyMigration{
		From: toml.Key{"server", "timeout"},
		To:   toml.Key{"server", "read_timeout"},
	})
	if err != nil {
		panic(err)
	}
	for _, m := range applied {
		fmt.Printf("migrated %s -> %s (line %d, field %q)\n", m.From, m.To, m.Position.Line, m.Field)
	}
	fmt.Printf("read_timeout = %d\n\n", conf.Server.ReadTimeout)

	// 2. Wildcard migration over an array of tables.
	var fleet Fleet
	_, applied, err = toml.NewDecoder(strings.NewReader(
		"[[servers]]\ntimeout = 1\n[[servers]]\ntimeout = 2\n")).DecodeWithKeyMigrations(&fleet, toml.KeyMigration{
		From: toml.Key{"servers", "*", "timeout"},
		To:   toml.Key{"servers", "*", "read_timeout"},
	})
	if err != nil {
		panic(err)
	}
	for _, m := range applied {
		fmt.Printf("migrated %s -> %s (element %d, line %d)\n", m.From, m.To, m.Index+1, m.Position.Line)
	}
	fmt.Printf("read_timeouts = %v\n\n", []int{fleet.Servers[0].ReadTimeout, fleet.Servers[1].ReadTimeout})

	// 3. Both the old and new key exist: conflict, no silent overwrite.
	var c Config
	_, _, err = toml.NewDecoder(strings.NewReader(
		"[server]\ntimeout = 30\nread_timeout = 60\n")).DecodeWithKeyMigrations(&c, toml.KeyMigration{
		From: toml.Key{"server", "timeout"},
		To:   toml.Key{"server", "read_timeout"},
	})
	var perr toml.ParseError
	if errors.As(err, &perr) {
		fmt.Println(perr.ErrorWithPosition())
	}

	// 4. A conflict inside an array of tables points out the element.
	var f Fleet
	_, _, err = toml.NewDecoder(strings.NewReader(
		"[[servers]]\ntimeout = 1\n[[servers]]\ntimeout = 2\nread_timeout = 3\n")).DecodeWithKeyMigrations(&f, toml.KeyMigration{
		From: toml.Key{"servers", "*", "timeout"},
		To:   toml.Key{"servers", "*", "read_timeout"},
	})
	if errors.As(err, &perr) {
		fmt.Println(perr.ErrorWithPosition())
	}
}
