// Command tomlcontract prints the configuration contract of a Go type as
// JSON, as derived by toml.DescribeType.
//
// It demonstrates object, table-array, scalar, map, reference and opaque
// nodes, including a recursive type and a custom Unmarshaler (which is
// marked opaque and never executed). Run it with:
//
//	go run ./cmd/tomlcontract
//
// It finishes in well under 90 seconds.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// opaqueSettings implements toml.Unmarshaler but not toml.TypeDescriber, so
// the contract marks it opaque without ever calling UnmarshalTOML.
type opaqueSettings struct{ calls int }

func (o *opaqueSettings) UnmarshalTOML(any) error { o.calls++; return nil }

type backend struct {
	Address string        `toml:"address"`
	Weight  int           `toml:"weight"`
	Timeout time.Duration `toml:"timeout"`
}

// pool is recursive: a fallback pool is a pool itself. The contract
// expresses this with a reference node instead of expanding forever.
type pool struct {
	Name     string   `toml:"name"`
	Backends []backend `toml:"backends"`
	Fallback *pool    `toml:"fallback"`
}

type server struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
}

type config struct {
	server          // anonymous: host and port are promoted
	Title    string            `toml:"title"`
	Started  time.Time         `toml:"started"`
	Main     pool              `toml:"main"`
	Labels   map[string]string `toml:"labels"`
	Settings *opaqueSettings   `toml:"settings"`
}

func main() {
	contract, err := toml.DescribeType(config{}, toml.DescribeConfig{
		Required: []toml.Key{{"title"}, {"main", "name"}},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "tomlcontract:", err)
		os.Exit(1)
	}

	out, err := json.MarshalIndent(contract, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tomlcontract:", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}

