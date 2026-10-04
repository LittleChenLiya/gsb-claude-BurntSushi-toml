package toml

import (
	"strings"
	"testing"
	"time"
)

// The four TOML date/time types round-trip through Decode and Encode with their
// category, offset, and fractional-second precision preserved.
func TestDateTimeRoundTrip(t *testing.T) {
	type doc struct {
		Offset  DateTime `toml:"offset"`
		LocalDT DateTime `toml:"local-datetime"`
		LocalD  DateTime `toml:"local-date"`
		LocalT  DateTime `toml:"local-time"`
	}

	var d doc
	_, err := Decode(`
offset          = 2020-01-02T03:04:05+08:00
local-datetime  = 2020-01-02T03:04:05
local-date      = 2020-01-02
local-time      = 03:04:05.120
`, &d)
	if err != nil {
		t.Fatal(err)
	}

	if d.Offset.Kind != OffsetDateTime {
		t.Errorf("offset: kind = %v, want OffsetDateTime", d.Offset.Kind)
	}
	if d.Offset.Offset != 8*3600 {
		t.Errorf("offset: offset = %d, want %d", d.Offset.Offset, 8*3600)
	}
	if d.LocalDT.Kind != LocalDateTime {
		t.Errorf("local-datetime: kind = %v, want LocalDateTime", d.LocalDT.Kind)
	}
	if d.LocalD.Kind != LocalDate {
		t.Errorf("local-date: kind = %v, want LocalDate", d.LocalD.Kind)
	}
	if d.LocalT.Kind != LocalTime {
		t.Errorf("local-time: kind = %v, want LocalTime", d.LocalT.Kind)
	}
	if d.LocalT.Precision != 3 {
		t.Errorf("local-time: precision = %d, want 3", d.LocalT.Precision)
	}
	if d.LocalT.Nanosecond != 120_000_000 {
		t.Errorf("local-time: nanosecond = %d, want 120000000", d.LocalT.Nanosecond)
	}

	want := "offset = 2020-01-02T03:04:05+08:00\n" +
		"local-datetime = 2020-01-02T03:04:05\n" +
		"local-date = 2020-01-02\n" +
		"local-time = 03:04:05.120\n"
	got, err := Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("round-trip mismatch:\n got: %s\nwant: %s", got, want)
	}
}

// Timezone-less values never pick up the machine's local timezone, no matter
// the TZ the process runs under. (Run this test with different TZ values.)
func TestDateTimeTimezoneIndependent(t *testing.T) {
	var d struct {
		DT DateTime
		D  DateTime
		T  DateTime
	}
	_, err := Decode(`
DT = 2020-01-02T03:04:05
D  = 2020-01-02
T  = 03:04:05.120
`, &d)
	if err != nil {
		t.Fatal(err)
	}

	if got := d.DT.String(); got != "2020-01-02T03:04:05" {
		t.Errorf("local datetime = %q, want 2020-01-02T03:04:05", got)
	}
	if got := d.D.String(); got != "2020-01-02" {
		t.Errorf("local date = %q, want 2020-01-02", got)
	}
	if got := d.T.String(); got != "03:04:05.120" {
		t.Errorf("local time = %q, want 03:04:05.120", got)
	}
	for _, s := range []string{d.DT.String(), d.D.String(), d.T.String()} {
		if strings.ContainsAny(s, "+Z") {
			t.Errorf("timezone-less value %q must not contain an offset", s)
		}
	}

	// The machine's zone must never be applied: local values convert to a
	// time.Time in UTC, not time.Local.
	if loc := d.DT.Time().Location(); loc != time.UTC {
		t.Errorf("local datetime Time() location = %v, want UTC", loc)
	}
}

// Carriers nest inside arrays (including nested arrays) and keep their type.
func TestDateTimeArrayNested(t *testing.T) {
	var d struct {
		Matrix [][]DateTime
	}
	_, err := Decode(`Matrix = [["2020-01-02T03:04:05", "03:04:05.120"], ["2020-01-02"]]`, &d)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Matrix) != 2 {
		t.Fatalf("len(Matrix) = %d, want 2", len(d.Matrix))
	}
	want := [][]string{
		{"2020-01-02T03:04:05", "03:04:05.120"},
		{"2020-01-02"},
	}
	for i, row := range want {
		if len(d.Matrix[i]) != len(row) {
			t.Fatalf("len(Matrix[%d]) = %d, want %d", i, len(d.Matrix[i]), len(row))
		}
		for j, w := range row {
			if got := d.Matrix[i][j].String(); got != w {
				t.Errorf("Matrix[%d][%d] = %q, want %q", i, j, got, w)
			}
		}
	}

	// Encoding the nested array yields native literals, not quoted strings.
	out, err := Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	wantEnc := "Matrix = [[2020-01-02T03:04:05, 03:04:05.120], [2020-01-02]]\n"
	if string(out) != wantEnc {
		t.Errorf("nested array encoding = %q, want %q", out, wantEnc)
	}
}

// Fractional seconds beyond 9 digits are explicitly rejected at the carrier
// entry point rather than silently truncated.
func TestDateTimeOverPrecisionRejected(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"2020-01-02T03:04:05.1234567890+08:00", "10 digits"},
		{"2020-01-02T03:04:05.1234567890", "10 digits"},
		{"03:04:05.1234567890", "10 digits"},
	} {
		var d struct{ V DateTime }
		_, err := Decode("V = "+tc.in, &d)
		if err == nil {
			t.Errorf("Decode(%q) succeeded, want error", tc.in)
			continue
		}
		if !strings.Contains(err.Error(), "at most 9 are supported") {
			t.Errorf("Decode(%q) error = %v, want over-precision rejection", tc.in, err)
		}
	}

	// Exactly 9 digits is accepted and preserved.
	var d struct{ V DateTime }
	if _, err := Decode("V = 03:04:05.123456789", &d); err != nil {
		t.Fatalf("Decode 9 digits: %v", err)
	}
	if got := d.V.String(); got != "03:04:05.123456789" {
		t.Errorf("9 digits = %q, want 03:04:05.123456789", got)
	}
}

// The carrier can be built directly from fields; encoding selects the
// corresponding TOML native literal for each of the four types.
func TestDateTimeFieldsAndOutputs(t *testing.T) {
	cases := []struct {
		name string
		dt   DateTime
		want string
	}{
		{
			name: "offset datetime",
			dt: DateTime{
				Kind: OffsetDateTime,
				Year: 2020, Month: 1, Day: 2,
				Hour: 3, Minute: 4, Second: 5,
				Offset: 8 * 3600,
			},
			want: "2020-01-02T03:04:05+08:00",
		},
		{
			name: "local datetime",
			dt: DateTime{
				Kind: LocalDateTime,
				Year: 2020, Month: 1, Day: 2,
				Hour: 3, Minute: 4, Second: 5,
			},
			want: "2020-01-02T03:04:05",
		},
		{
			name: "local date",
			dt:   DateTime{Kind: LocalDate, Year: 2020, Month: 1, Day: 2},
			want: "2020-01-02",
		},
		{
			name: "local time",
			dt: DateTime{
				Kind: LocalTime, Hour: 3, Minute: 4, Second: 5,
				Nanosecond: 120_000_000, Precision: 3,
			},
			want: "03:04:05.120",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.dt.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
			out, err := Marshal(struct{ V DateTime }{tc.dt})
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(out)); got != "V = "+tc.want {
				t.Errorf("Marshal = %q, want %q", got, "V = "+tc.want)
			}
		})
	}
}

// ParseDateTime classifies each shape correctly and rejects invalid input.
func TestDateTimeParse(t *testing.T) {
	ok := []struct {
		in   string
		kind DateTimeKind
	}{
		{"2020-01-02T03:04:05+08:00", OffsetDateTime},
		{"2020-01-02T03:04:05Z", OffsetDateTime},
		{"2020-01-02t03:04:05z", OffsetDateTime},
		{"2020-01-02 03:04:05", LocalDateTime},
		{"2020-01-02T03:04:05", LocalDateTime},
		{"2020-01-02", LocalDate},
		{"03:04:05", LocalTime},
		{"03:04:05.120", LocalTime},
	}
	for _, tc := range ok {
		dt, err := ParseDateTime(tc.in)
		if err != nil {
			t.Errorf("ParseDateTime(%q): %v", tc.in, err)
			continue
		}
		if dt.Kind != tc.kind {
			t.Errorf("ParseDateTime(%q) kind = %v, want %v", tc.in, dt.Kind, tc.kind)
		}
	}

	bad := []string{
		"",
		"2020-13-02",
		"2020-02-30",
		"2020-01-02T25:04:05",
		"2020-01-02T03:04",
		"03:04",
		"03:04:05.",
		"03:04:05.1234567890",
		"2020-01-02T03:04:05+25:00",
		"03:04:05+08:00",
		"garbage",
	}
	for _, in := range bad {
		if _, err := ParseDateTime(in); err == nil {
			t.Errorf("ParseDateTime(%q) succeeded, want error", in)
		}
	}
}

// The default time.Time mapping is unchanged: decoding into interface{} still
// yields a time.Time, and decoding into a time.Time field still works.
func TestDateTimeDefaultMappingUnchanged(t *testing.T) {
	var v any
	if _, err := Decode("V = 2020-01-02T03:04:05+08:00", &v); err != nil {
		t.Fatal(err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("decode into any gives %T, want map[string]any", v)
	}
	if _, ok := m["V"].(time.Time); !ok {
		t.Errorf("decode into any gives %T, want time.Time", m["V"])
	}

	var d struct{ V time.Time }
	if _, err := Decode("V = 2020-01-02T03:04:05+08:00", &d); err != nil {
		t.Fatal(err)
	}
	if d.V.Location() == nil {
		t.Error("time.Time location is nil")
	}
}

// FromTime preserves the offset and nanosecond precision.
func TestFromTime(t *testing.T) {
	loc := time.FixedZone("test", -5*3600)
	tm := time.Date(2020, 1, 2, 3, 4, 5, 120_000_000, loc)
	dt := FromTime(tm)
	if dt.Kind != OffsetDateTime {
		t.Errorf("kind = %v, want OffsetDateTime", dt.Kind)
	}
	if dt.Offset != -5*3600 {
		t.Errorf("offset = %d, want %d", dt.Offset, -5*3600)
	}
	if got := dt.String(); got != "2020-01-02T03:04:05.12-05:00" {
		t.Errorf("String() = %q, want 2020-01-02T03:04:05.12-05:00", got)
	}
	if !dt.Time().Equal(tm) {
		t.Errorf("Time() = %v, want %v", dt.Time(), tm)
	}
}
