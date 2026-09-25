// Command tomlcontract prints the configuration contract of an example Go
// type as JSON, demonstrating toml.DescribeType: key names and types for
// editor tooling, reference nodes for recursive types, and opaque nodes for
// custom Unmarshaler types.
//
// Usage:
//
//	go run ./cmd/tomlcontract
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// credentials is a custom Unmarshaler without a toml.TypeDescriber
// callback, so it shows up as an opaque node in the contract.
type credentials struct{ user, pass string }

func (c *credentials) UnmarshalTOML(v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("expected table")
	}
	c.user, _ = m["user"].(string)
	c.pass, _ = m["pass"].(string)
	return nil
}

type server struct {
	Host string    `toml:"host"`
	Port int       `toml:"port"`
	TLS  *tls      `toml:"tls"`
	When time.Time `toml:"when"`
	Tags []string  `toml:"tags"`
}

type tls struct {
	Enabled bool   `toml:"enabled"`
	Cert    string `toml:"cert"`
}

type upstream struct {
	Name     string    `toml:"name"`
	Fallback *upstream `toml:"fallback"` // recursive: becomes a ref node
}

type config struct {
	server // anonymous: host, port, tls, when, tags are promoted

	Title   string            `toml:"title"`
	Auth    credentials       `toml:"auth"` // opaque node
	Labels  map[string]string `toml:"labels"`
	Up      []upstream        `toml:"upstreams"`
	Comment string            `toml:"-"`
}

func main() {
	c, err := toml.DescribeType(config{}, &toml.DescribeOptions{
		Required: []toml.Key{{"title"}, {"host"}},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
