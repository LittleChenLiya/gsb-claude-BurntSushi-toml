# tomlcontract

Demonstrates `toml.DescribeType`: prints the configuration contract of an
example Go type as JSON, including key names and types for editor tooling,
reference nodes for recursive types, and opaque nodes for custom
`toml.Unmarshaler` types.

```
$ go run ./cmd/tomlcontract
```

