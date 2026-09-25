TOML stands for Tom's Obvious, Minimal Language. This Go package provides a
reflection interface similar to Go's standard library `json` and `xml` packages.

Compatible with TOML version [v1.1.0](https://toml.io/en/v1.1.0).

Documentation: https://pkg.go.dev/github.com/BurntSushi/toml

See the [releases page](https://github.com/BurntSushi/toml/releases) for a
changelog; this information is also in the git tag annotations (e.g. `git show
v0.4.0`).

This library requires Go 1.19 or newer; add it to your go.mod with:

    % go get github.com/BurntSushi/toml@latest

It also comes with a TOML validator CLI tool:

    % go install github.com/BurntSushi/toml/cmd/tomlv@latest
    % tomlv some-toml-file.toml

### Examples
For the simplest example, consider some TOML file as just a list of keys and
values:

```toml
Age = 25
Cats = [ "Cauchy", "Plato" ]
Pi = 3.14
Perfection = [ 6, 28, 496, 8128 ]
DOB = 1987-07-05T05:45:00Z
```

Which can be decoded with:

```go
type Config struct {
	Age        int
	Cats       []string
	Pi         float64
	Perfection []int
	DOB        time.Time
}

var conf Config
_, err := toml.Decode(tomlData, &conf)
```

You can also use struct tags if your struct field name doesn't map to a TOML key
value directly:

```toml
some_key_NAME = "wat"
```

```go
type TOML struct {
    ObscureKey string `toml:"some_key_NAME"`
}
```

Beware that like other decoders **only exported fields** are considered when
encoding and decoding; private fields are silently ignored.

### Key migration
`DecodeWithMigrations` decodes while renaming keys on the fly: callers pass a
list of `KeyMigration` values mapping old key paths to new ones, the keys are
migrated in the parsed document, and the result is decoded with the regular
decoding rules. Plain `Decode` never applies migrations.

Paths are slices of key segments, so a literal quoted key such as
`"a.b"` is a single segment and is never confused with a nested path. The
special `toml.Wildcard` segment matches any single key, or every element of an
array of tables, migrating each element individually:

```go
var conf Config
md, err := toml.DecodeWithMigrations(tomlData, &conf,
	toml.KeyMigration{
		From: toml.Key{"server", "timeout"},
		To:   toml.Key{"server", "read_timeout"},
	},
	// Migrate "timeout" in every element of the "servers" array of tables.
	toml.KeyMigration{
		From: toml.Key{"servers", toml.Wildcard, "timeout"},
		To:   toml.Key{"servers", toml.Wildcard, "read_timeout"},
	},
)
```

Every applied migration is recorded in `md.Migrations` with the source
position and target field. It is an error (never a silent overwrite) when the
old and new key both exist, when migrations form a chain or cycle, or when the
target path leads into a non-table value such as a scalar. The input document
is never modified. See `_example/migrate/` for a runnable demonstration.

### Using the `Marshaler` and `encoding.TextUnmarshaler` interfaces
Here's an example that automatically parses values in a `mail.Address`:

```toml
contacts = [
    "Donald Duck <donald@duckburg.com>",
    "Scrooge McDuck <scrooge@duckburg.com>",
]
```

Can be decoded with:

```go
// Create address type which satisfies the encoding.TextUnmarshaler interface.
type address struct {
	*mail.Address
}

func (a *address) UnmarshalText(text []byte) error {
	var err error
	a.Address, err = mail.ParseAddress(string(text))
	return err
}

// Decode it.
func decode() {
	blob := `
		contacts = [
			"Donald Duck <donald@duckburg.com>",
			"Scrooge McDuck <scrooge@duckburg.com>",
		]
	`

	var contacts struct {
		Contacts []address
	}

	_, err := toml.Decode(blob, &contacts)
	if err != nil {
		log.Fatal(err)
	}

	for _, c := range contacts.Contacts {
		fmt.Printf("%#v\n", c.Address)
	}

	// Output:
	// &mail.Address{Name:"Donald Duck", Address:"donald@duckburg.com"}
	// &mail.Address{Name:"Scrooge McDuck", Address:"scrooge@duckburg.com"}
}
```

To target TOML specifically you can implement the `UnmarshalTOML` interface in
a similar way.

### More complex usage
See the [`_example/`](/_example) directory for a more complex example.
