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

### Resource limits
`Decoder` offers a set of optional resource limits to guard against
pathological or hostile input. All of them are disabled by default (a limit of
`0`), so existing `Decode` calls behave exactly as before; negative values are
rejected with an error:

```go
dec := toml.NewDecoder(r)
dec.MaxInputBytes(1 << 20)  // raw input bytes
dec.MaxKeyAssignments(1000) // "key = value" assignments
dec.MaxStringBytes(1 << 16) // decoded size of a single string
dec.MaxArrayElements(10000) // total array elements
_, err := dec.Decode(&conf)
```

The limits are defined as follows:

- `MaxInputBytes` counts the raw TOML bytes consumed from the reader (a
  leading BOM is not counted).
- `MaxKeyAssignments` counts every `key = value` assignment, including
  assignments inside inline tables; table headers (`[table]`,
  `[[array-of-tables]]`) are not assignments and are not counted.
- `MaxStringBytes` counts the UTF-8 bytes of each decoded string: for basic
  strings after escape expansion, and for multiline strings after the
  whitespace the TOML spec strips (the first newline and whitespace after
  line-ending backslashes) has been removed.
- `MaxArrayElements` counts every element of every `[ ... ]` array in the
  document; a nested array or inline table counts as one element of its parent
  array (its own contents are counted separately), and every
  `[[array-of-tables]]` entry counts as one element.

A value exactly equal to the limit is accepted; the first position beyond the
limit is reported as a `toml.LimitError` (wrapping the limit name and limit
value) inside a `toml.ParseError` with the line and column. Limits are
enforced while lexing and parsing: the input is read incrementally in bounded
chunks (never more than 32 KiB per `Read` call), oversized decoded strings are
never allocated, and reading stops as soon as a limit is exceeded. When a
limit is exceeded the decode target is left completely untouched. If both
`MaxDepth` and a resource limit apply, whichever is hit first in the document
is reported.

`DecodeContext` additionally stops decoding when a `context.Context` is
cancelled; errors from the underlying reader and from context cancellation
are returned wrapped with `%w`, so they stay identifiable with
`errors.Is`/`errors.As`.

### More complex usage
See the [`_example/`](/_example) directory for a more complex example.
