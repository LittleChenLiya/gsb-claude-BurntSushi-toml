// Package toml implements decoding and encoding of TOML files.
//
// This package supports TOML v1.0.0, as specified at https://toml.io
//
// The github.com/BurntSushi/toml/cmd/tomlv package implements a TOML validator,
// and can be used to verify if TOML document is valid. It can also be used to
// print the type of each key.
//
// [DescribeType] exports a serializable configuration contract for any Go
// type supported by [Decoder], for editor key and type hints; see
// github.com/BurntSushi/toml/cmd/tomlcontract for a demonstration.
package toml

