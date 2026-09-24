package toml_test

import (
	"errors"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// Decode a small document well within the configured resource limits.
func ExampleDecoder_resourceLimits() {
	var conf struct {
		Name string
		Tags []string
	}

	dec := toml.NewDecoder(strings.NewReader("name = \"toml\"\ntags = [\"a\", \"b\"]\n"))
	dec.MaxInputBytes(1 << 20)
	dec.MaxKeyAssignments(100)
	dec.MaxStringBytes(16)
	dec.MaxArrayElements(8)

	if _, err := dec.Decode(&conf); err != nil {
		panic(err)
	}
	fmt.Println(conf.Name, conf.Tags)

	// Output: toml [a b]
}

// A document exceeding a resource limit fails with a toml.LimitError wrapped
// in a toml.ParseError holding the line and column of the first position
// that exceeds the limit.
func ExampleDecoder_resourceLimitsExceeded() {
	var conf struct {
		Name string
	}

	dec := toml.NewDecoder(strings.NewReader("name = \"this string is way too long\""))
	dec.MaxStringBytes(4)
	_, err := dec.Decode(&conf)

	var limitErr toml.LimitError
	if errors.As(err, &limitErr) {
		fmt.Printf("%s allows %d bytes\n", limitErr.Name, limitErr.Limit)
	}
	var parseErr toml.ParseError
	if errors.As(err, &parseErr) {
		fmt.Printf("at line %d, column %d\n", parseErr.Position.Line, parseErr.Position.Col)
	}
	fmt.Println(err)

	// Output:
	// MaxStringBytes allows 4 bytes
	// at line 1, column 13
	// toml: line 1 (last key "name"): resource limit MaxStringBytes exceeded: limit is 4
}
