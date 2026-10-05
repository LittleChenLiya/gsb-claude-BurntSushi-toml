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

### Unknown key policies
By default unknown keys — keys in the TOML document that don't map to a struct
field or map key — are silently ignored and only observable through
`MetaData.Undecoded()`. With `Decoder.Policy` you can instead decide per key
path whether unknown keys are rejected, kept, or ignored:

```toml
name = "app"
y    = 1            # unknown top-level key

[server]
host = "localhost"
x    = 2020-01-02T03:04:05Z   # unknown key inside [server]
```

```go
var conf struct {
	Name   string `toml:"name"`
	Server struct {
		Host string `toml:"host"`
	} `toml:"server"`
}

md, err := toml.NewDecoder(r).Policy(toml.DecodePolicy{
	Default: toml.RejectUnknown, // reject unknown keys such as "y" ...
	Rules: []toml.PolicyRule{
		// ... but keep unknown keys below [server], such as "server.x".
		{Path: toml.Key{"server"}, Action: toml.KeepUnknown},
	},
}).Decode(&conf)
// err is a toml.UnknownKeyError listing "y"; conf is left untouched.
// md.Kept() is map[string]any{"server": map[string]any{"x": time.Time(...)}}
```

Kept values are stored in a separate sidecar tree (`MetaData.Kept()`) with
their TOML types preserved (`int64`, `float64`, `string`, `bool`, `time.Time`,
`map[string]any`, `[]any`); they are never written to the decode target. On
rejection the error lists every rejected path and the target object is not
delivered. Keys consumed by a custom `UnmarshalTOML` decoder are never
reported as unknown.

Rule priority: the rule with the longest matching path prefix wins; on equal
path lengths the later rule wins; keys not matched by any rule use `Default`
(the zero value is `IgnoreUnknown`, the historical behaviour). Rule paths
follow TOML key syntax, so a dot inside a quoted key name is not a path
separator: use `toml.ParseKeyPath("server.\"a.b\"")` or `toml.Key{"server",
"a.b"}` to match a key literally named `a.b`.

### More complex usage
See the [`_example/`](/_example) directory for a more complex example.
