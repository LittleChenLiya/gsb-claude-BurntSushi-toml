package toml_test

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// A shared sub-object referenced from multiple branches is encoded normally;
// only values that refer back to themselves are rejected.
func ExampleSafeEncoder() {
	type inner struct {
		N int `toml:"n"`
	}
	shared := &inner{N: 42}
	v := map[string]any{"a": shared, "b": shared}

	if err := toml.NewSafeEncoder(os.Stdout).Encode(v); err != nil {
		fmt.Println("error:", err)
	}

	// Output:
	// [a]
	//   n = 42
	//
	// [b]
	//   n = 42
}

// Cyclic values are detected before anything is written, and the error
// reports the path at which the cycle was found.
func ExampleSafeEncoder_cyclic() {
	type node struct {
		Name string `toml:"name"`
		Next *node  `toml:"next"`
	}
	n := &node{Name: "x"}
	n.Next = n

	err := toml.NewSafeEncoder(os.Stdout).Encode(map[string]any{"top": n})
	fmt.Println(err)

	// Output:
	// toml: cyclic reference detected at key 'top.next'
}
