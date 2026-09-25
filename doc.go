// Package toml implements decoding and encoding of TOML files.
//
// This package supports TOML v1.0.0, as specified at https://toml.io
//
// The github.com/BurntSushi/toml/cmd/tomlv package implements a TOML validator,
// and can be used to verify if TOML document is valid. It can also be used to
// print the type of each key.
//
// DescribeType derives a serializable configuration contract from a Go
// type, using the same field resolution rules as Decode. It is intended
// for editor tooling that hints key names and types. See
// cmd/tomlcontract for a demonstration.
package toml
