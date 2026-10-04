package toml

import (
	"strings"
	"testing"
	"time"
)

// The four TOML datetime types round-trip through the DateTime carrier with
// their kind, offset, and fractional-second precision intact.
func TestDateTimeRoundTrip(t *testing.T) {
	var v struct {
		Offset DateTime `toml:"offset"`
		Local  DateTime `toml:"local"`
		Date   DateTime `toml:"date"`
		Time   DateTime `toml:"time"`
	}
	input := `
offset = 2020-01-02T03:04:05+08:00
local  = 2020-01-02T03:04:05
date   = 2020-01-02
time   = 03:04:05.120
`
	if _, err := Decode(input, &v); err != nil {
		t.Fatal(err)
	}

	if v.Offset.Kind != OffsetDateTime {
		t.Errorf("offset: kind is %v, want OffsetDateTime", v.Offset.Kind)
	}
	if v.Offset.Offset != 8*60 {
		t.Errorf("offset: offset is %d minutes, want %d", v.Offset.Offset, 8*60)
	}
	if v.Local.Kind != LocalDateTime {
		t.Errorf("local: kind is %v, want LocalDateTime", v.Local.Kind)
	}
	if v.Local.Offset != 0 {
		t.Errorf("local: offset is %d, want 0 (no local timezone attached)", v.Local.Offset)
	}
	if v.Date.Kind != LocalDate {
		t.Errorf("date: kind is %v, want LocalDate", v.Date.Kind)
	}
	if v.Time.Kind != LocalTime {
		t.Errorf("time: kind is %v, want LocalTime", v.Time.Kind)
	}
	if v.Time.Nanosecond != 120000000 || v.Time.Precision != 3 {
		t.Errorf("time: got %d ns with precision %d, want 120000000 ns with precision 3",
			v.Time.Nanosecond, v.Time.Precision)
	}

	// Re-encoding must produce the same native literals: same kinds, same
	// offset, same three-digit precision, and no quoted strings.
	out, err := Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	want := `offset = 2020-01-02T03:04:05+08:00
local = 2020-01-02T03:04:05
date = 2020-01-02
time = 03:04:05.120
`
	if string(out) != want {
		t.Errorf("encoded output mismatch:\ngot:\n%s\nwant:\n%s", out, want)
	}
	if strings.Contains(string(out), `"`) {
		t.Errorf("datetime was encoded as a quoted string:\n%s", out)
	}

	// And decoding that output again gives the same values.
	var v2 struct {
		Offset DateTime `toml:"offset"`
		Local  DateTime `toml:"local"`
		Date   DateTime `toml:"date"`
		Time   DateTime `toml:"time"`
	}
	if _, err := Decode(string(out), &v2); err != nil {
		t.Fatal(err)
	}
	if v != v2 {
		t.Errorf("round-trip mismatch:\ngot:  %+v\nwant: %+v", v2, v)
	}
}

// Fractional-second precision from 0 to 9 digits is preserved exactly.
func TestDateTimePrecision(t *testing.T) {
	cases := []struct {
		lit       string
		precision int
		nsec      int
	}{
		{"03:04:05", 0, 0},
		{"03:04:05.1", 1, 100000000},
		{"03:04:05.12", 2, 120000000},
		{"03:04:05.120", 3, 120000000},
		{"03:04:05.1234", 4, 123400000},
		{"03:04:05.12345", 5, 123450000},
		{"03:04:05.123456", 6, 123456000},
		{"03:04:05.1234567", 7, 123456700},
		{"03:04:05.12345678", 8, 123456780},
		{"03:04:05.123456789", 9, 123456789},
	}
	for _, c := range cases {
		var v struct {
			T DateTime `toml:"t"`
		}
		if _, err := Decode("t = "+c.lit+"\n", &v); err != nil {
			t.Errorf("%s: %s", c.lit, err)
			continue
		}
		if v.T.Precision != c.precision || v.T.Nanosecond != c.nsec {
			t.Errorf("%s: got precision %d, %d ns; want precision %d, %d ns",
				c.lit, v.T.Precision, v.T.Nanosecond, c.precision, c.nsec)
		}
		out, err := Marshal(v)
		if err != nil {
			t.Errorf("%s: %s", c.lit, err)
			continue
		}
		if want := "t = " + c.lit + "\n"; string(out) != want {
			t.Errorf("%s: encoded as %q, want %q", c.lit, out, want)
		}
	}
}

// More than 9 fractional-second digits is explicitly rejected by the DateTime
// carrier instead of being silently truncated; the default time.Time mapping
// keeps its existing truncating behaviour.
func TestDateTimeTooMuchPrecision(t *testing.T) {
	doc := "t = 03:04:05.1234567891\n"

	var v struct {
		T DateTime `toml:"t"`
	}
	_, err := Decode(doc, &v)
	if err == nil {
		t.Fatal("expected an error for 10 fractional-second digits, got none")
	}
	if !strings.Contains(err.Error(), "maximum supported is 9") {
		t.Errorf("unexpected error: %s", err)
	}

	// Default mapping is unchanged: time.Time silently truncates, as before.
	var m struct {
		T time.Time `toml:"t"`
	}
	if _, err := Decode(doc, &m); err != nil {
		t.Fatalf("default time.Time mapping broke: %s", err)
	}
	if m.T.Nanosecond() != 123456789 {
		t.Errorf("default mapping: got %d ns, want truncated 123456789", m.T.Nanosecond())
	}
}

// Changing the machine's local timezone must not affect timezone-less
// (local) values: they never get a local offset attached.
func TestDateTimeLocalTimezoneIndependent(t *testing.T) {
	doc := `
offset = 2020-01-02T03:04:05+08:00
local  = 2020-01-02T03:04:05
date   = 2020-01-02
time   = 03:04:05.120
`
	decode := func() (string, error) {
		var v struct {
			Offset DateTime `toml:"offset"`
			Local  DateTime `toml:"local"`
			Date   DateTime `toml:"date"`
			Time   DateTime `toml:"time"`
		}
		if _, err := Decode(doc, &v); err != nil {
			return "", err
		}
		out, err := Marshal(v)
		return string(out), err
	}

	origLocal := time.Local
	defer func() { time.Local = origLocal }()

	time.Local = time.FixedZone("UTC+14", 14*60*60)
	outPlus, err := decode()
	if err != nil {
		t.Fatal(err)
	}
	time.Local = time.FixedZone("UTC-12", -12*60*60)
	outMinus, err := decode()
	if err != nil {
		t.Fatal(err)
	}

	if outPlus != outMinus {
		t.Errorf("local timezone changed the result:\nUTC+14:\n%s\nUTC-12:\n%s", outPlus, outMinus)
	}
	want := `offset = 2020-01-02T03:04:05+08:00
local = 2020-01-02T03:04:05
date = 2020-01-02
time = 03:04:05.120
`
	if outPlus != want {
		t.Errorf("got:\n%s\nwant:\n%s", outPlus, want)
	}
}

// DateTime works as a slice element, including nested slices.
func TestDateTimeArrays(t *testing.T) {
	var v struct {
		List   []DateTime   `toml:"list"`
		Nested [][]DateTime `toml:"nested"`
	}
	input := `
list = [2020-01-02T03:04:05+08:00, 2020-01-02T03:04:06+08:00]
nested = [[2020-01-02, 2020-01-03], [03:04:05, 03:04:05.120]]
`
	if _, err := Decode(input, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.List) != 2 || v.List[0].Kind != OffsetDateTime || v.List[1].Offset != 8*60 {
		t.Errorf("list: %+v", v.List)
	}
	if len(v.Nested) != 2 || len(v.Nested[0]) != 2 || len(v.Nested[1]) != 2 {
		t.Fatalf("nested: %+v", v.Nested)
	}
	if v.Nested[0][0].Kind != LocalDate || v.Nested[0][1].Day != 3 {
		t.Errorf("nested[0]: %+v", v.Nested[0])
	}
	if v.Nested[1][1].Kind != LocalTime || v.Nested[1][1].Precision != 3 {
		t.Errorf("nested[1]: %+v", v.Nested[1])
	}

	out, err := Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	want := `list = [2020-01-02T03:04:05+08:00, 2020-01-02T03:04:06+08:00]
nested = [[2020-01-02, 2020-01-03], [03:04:05, 03:04:05.120]]
`
	if string(out) != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}

	var v2 struct {
		List   []DateTime   `toml:"list"`
		Nested [][]DateTime `toml:"nested"`
	}
	if _, err := Decode(string(out), &v2); err != nil {
		t.Fatal(err)
	}
	if len(v2.List) != 2 || len(v2.Nested) != 2 || v2.Nested[1][1] != v.Nested[1][1] {
		t.Errorf("round-trip mismatch: %+v", v2)
	}
}

// ParseDateTime is the explicit entry point for single TOML datetime
// literals.
func TestParseDateTime(t *testing.T) {
	cases := []struct {
		lit  string
		want DateTime
	}{
		{"2020-01-02T03:04:05+08:00", DateTime{Kind: OffsetDateTime, Year: 2020, Month: 1, Day: 2, Hour: 3, Minute: 4, Second: 5, Offset: 480}},
		{"2020-01-02T03:04:05Z", DateTime{Kind: OffsetDateTime, Year: 2020, Month: 1, Day: 2, Hour: 3, Minute: 4, Second: 5}},
		{"2020-01-02T03:04:05.5-07:30", DateTime{Kind: OffsetDateTime, Year: 2020, Month: 1, Day: 2, Hour: 3, Minute: 4, Second: 5, Nanosecond: 500000000, Precision: 1, Offset: -450}},
		{"2020-01-02 03:04:05+08:00", DateTime{Kind: OffsetDateTime, Year: 2020, Month: 1, Day: 2, Hour: 3, Minute: 4, Second: 5, Offset: 480}}, // space separator
		{"2020-01-02t03:04:05z", DateTime{Kind: OffsetDateTime, Year: 2020, Month: 1, Day: 2, Hour: 3, Minute: 4, Second: 5}},                   // lowercase t/z
		{"2020-01-02T03:04:05", DateTime{Kind: LocalDateTime, Year: 2020, Month: 1, Day: 2, Hour: 3, Minute: 4, Second: 5}},
		{"2020-01-02", DateTime{Kind: LocalDate, Year: 2020, Month: 1, Day: 2}},
		{"03:04:05.120", DateTime{Kind: LocalTime, Hour: 3, Minute: 4, Second: 5, Nanosecond: 120000000, Precision: 3}},
	}
	for _, c := range cases {
		have, err := ParseDateTime(c.lit)
		if err != nil {
			t.Errorf("%s: %s", c.lit, err)
			continue
		}
		if have != c.want {
			t.Errorf("%s:\nhave: %+v\nwant: %+v", c.lit, have, c.want)
		}
	}

	for _, bad := range []string{
		"", "2020-01-02T", "2020-13-02", "2020-01-32", "25:04:05", "03:04:61",
		"03:04:05.", "03:04:05+08:00", // offset on a local time is invalid
		"2020-01-02T03:04:05+24:00", "not-a-datetime",
	} {
		if _, err := ParseDateTime(bad); err == nil {
			t.Errorf("%s: expected an error, got none", bad)
		}
	}

	if _, err := ParseDateTime("03:04:05.1234567891"); err == nil {
		t.Error("expected an error for 10 fractional-second digits, got none")
	}
}

// DateTime values survive a trip through Primitive/PrimitiveDecode, and a
// Primitive holding a datetime still encodes as a native literal.
func TestDateTimePrimitive(t *testing.T) {
	var v struct {
		T Primitive `toml:"t"`
	}
	md, err := Decode("t = 03:04:05.120\n", &v)
	if err != nil {
		t.Fatal(err)
	}

	var dt DateTime
	if err := md.PrimitiveDecode(v.T, &dt); err != nil {
		t.Fatal(err)
	}
	if dt.Kind != LocalTime || dt.Precision != 3 || dt.Nanosecond != 120000000 {
		t.Errorf("got %+v", dt)
	}

	// Re-encoding the Primitive itself keeps the default time.Time mapping,
	// which trims trailing zeros in the fractional seconds.
	out, err := Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if want := "t = 03:04:05.12\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

// Time converts back to time.Time using the same conventions as the default
// mapping, and encoding that time.Time yields the same kind of literal.
func TestDateTimeTime(t *testing.T) {
	// Note: the default time.Time mapping trims trailing zeros in the
	// fractional seconds (".120" → ".12"); only the DateTime carrier
	// preserves the exact precision.
	cases := []struct {
		lit     string
		timeEnc string // default time.Time mapping output
	}{
		{"2020-01-02T03:04:05+08:00", "2020-01-02T03:04:05+08:00"},
		{"2020-01-02T03:04:05", "2020-01-02T03:04:05"},
		{"2020-01-02", "2020-01-02"},
		{"03:04:05.120", "03:04:05.12"},
	}
	for _, c := range cases {
		dt, err := ParseDateTime(c.lit)
		if err != nil {
			t.Fatal(err)
		}
		tm := dt.Time()
		out, err := Marshal(struct{ T time.Time }{tm})
		if err != nil {
			t.Fatal(err)
		}
		if want := "T = " + c.timeEnc + "\n"; string(out) != want {
			t.Errorf("%s: got %q, want %q", c.lit, out, want)
		}
		// Converting a time.Time with the same location conventions back
		// gives an equal DateTime, except that the precision is inferred as
		// the minimal number of digits (".120" → ".12"), since time.Time
		// does not retain the original number of fractional digits.
		var back DateTime
		if err := back.UnmarshalTOML(tm); err != nil {
			t.Fatal(err)
		}
		wantBack := dt
		if c.lit == "03:04:05.120" {
			wantBack.Precision = 2
		}
		if back != wantBack {
			t.Errorf("%s: got %+v, want %+v", c.lit, back, wantBack)
		}
	}
}
