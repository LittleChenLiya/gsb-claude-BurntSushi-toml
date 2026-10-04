package toml

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml/internal"
)

// DateTimeKind identifies which of the four TOML datetime types a [DateTime]
// holds.
type DateTimeKind int

const (
	// OffsetDateTime is a TOML offset datetime, e.g. 2020-01-02T03:04:05+08:00
	OffsetDateTime DateTimeKind = iota + 1
	// LocalDateTime is a TOML local datetime, e.g. 2020-01-02T03:04:05
	LocalDateTime
	// LocalDate is a TOML local date, e.g. 2020-01-02
	LocalDate
	// LocalTime is a TOML local time, e.g. 03:04:05.120
	LocalTime
)

func (k DateTimeKind) String() string {
	switch k {
	case OffsetDateTime:
		return "offset datetime"
	case LocalDateTime:
		return "local datetime"
	case LocalDate:
		return "local date"
	case LocalTime:
		return "local time"
	}
	return "unknown datetime"
}

// DateTime is an explicit carrier for TOML datetime values. It distinguishes
// the four TOML datetime types (offset datetime, local datetime, local date,
// and local time) and preserves the information that [time.Time] loses:
//
//   - the original zone offset of an offset datetime, and
//   - the number of fractional-second digits as written (Precision, 0-9).
//
// Decoding a TOML datetime into a DateTime never attaches the local timezone
// to local (timezone-less) values, and encoding a DateTime always emits the
// corresponding native TOML datetime literal - never a quoted string.
//
// DateTime integrates with the standard [Decode]/[Encode] functions through
// the [Unmarshaler] and [Marshaler] interfaces, so it can be used directly as
// a struct field, map value, or slice element. The default mapping of TOML
// datetimes to [time.Time] is unchanged.
//
// Fractional seconds with more than 9 digits are rejected with an error
// rather than silently truncated like the default time.Time mapping.
type DateTime struct {
	Kind DateTimeKind // Which of the four TOML datetime types this is.

	Year  int // Year; e.g. 2020. Unused for LocalTime.
	Month int // Month; 1-12. Unused for LocalTime.
	Day   int // Day; 1-31. Unused for LocalTime.

	Hour   int // Hour; 0-23. Unused for LocalDate.
	Minute int // Minute; 0-59. Unused for LocalDate.
	Second int // Second; 0-59. Unused for LocalDate.

	// Nanosecond is the fractional second within the second, 0-999999999.
	// Unused for LocalDate.
	Nanosecond int

	// Precision is the number of fractional-second digits (0-9) as written
	// in the TOML document; it is preserved on a decode-encode round-trip.
	// For example "03:04:05.120" has Nanosecond 120000000 and Precision 3.
	Precision int

	// Offset is the zone offset in minutes east of UTC. It is only
	// meaningful for OffsetDateTime; local types always have a zero Offset
	// and never get the local timezone attached.
	Offset int
}

var (
	dateTimeType      = reflect.TypeOf(DateTime{})
	datetimeValueType = reflect.TypeOf(datetimeValue{})
)

// datetimeValue is the internal representation the parser produces for TOML
// datetimes: the default time.Time mapping plus the original (normalized)
// literal, so the DateTime carrier can recover the exact offset and
// fractional-second precision. Anything not decoding into a DateTime gets
// the plain time.Time, keeping the default mapping unchanged.
type datetimeValue struct {
	t   time.Time
	raw string
}

// ParseDateTime parses a single TOML datetime literal (offset datetime, local
// datetime, local date, or local time) into a [DateTime].
//
// Fractional seconds with more than 9 digits are rejected with an error.
func ParseDateTime(s string) (DateTime, error) {
	return parseDateTimeLiteral(s)
}

// Time converts the DateTime to a [time.Time]. Offset datetimes get a fixed
// zone with the stored offset; local types get the same internal locations
// the default time.Time mapping uses, so encoding the result produces the
// same kind of TOML datetime.
func (d DateTime) Time() time.Time {
	switch d.Kind {
	case OffsetDateTime:
		return time.Date(d.Year, time.Month(d.Month), d.Day,
			d.Hour, d.Minute, d.Second, d.Nanosecond,
			time.FixedZone("", d.Offset*60))
	case LocalDateTime:
		return time.Date(d.Year, time.Month(d.Month), d.Day,
			d.Hour, d.Minute, d.Second, d.Nanosecond, internal.LocalDatetime)
	case LocalDate:
		return time.Date(d.Year, time.Month(d.Month), d.Day,
			0, 0, 0, 0, internal.LocalDate)
	case LocalTime:
		return time.Date(0, 1, 1,
			d.Hour, d.Minute, d.Second, d.Nanosecond, internal.LocalTime)
	}
	return time.Time{}
}

// String returns the TOML datetime literal for this DateTime.
func (d DateTime) String() string {
	s, err := d.MarshalTOML()
	if err != nil {
		return "DateTime(" + err.Error() + ")"
	}
	return string(s)
}

// UnmarshalTOML implements the [Unmarshaler] interface, integrating DateTime
// with the standard [Decode] function.
func (d *DateTime) UnmarshalTOML(v any) error {
	switch v := v.(type) {
	case datetimeValue: // Directly from the parser: full-fidelity literal.
		dt, err := parseDateTimeLiteral(v.raw)
		if err != nil {
			return err
		}
		*d = dt
		return nil
	case time.Time: // e.g. via PrimitiveDecode or a manually built value.
		*d = dateTimeFromTime(v)
		return nil
	case string:
		dt, err := parseDateTimeLiteral(v)
		if err != nil {
			return err
		}
		*d = dt
		return nil
	default:
		return fmt.Errorf("toml: cannot decode TOML value of type %s into a DateTime", fmtType(v))
	}
}

// MarshalTOML implements the [Marshaler] interface, integrating DateTime with
// the standard [Encode] function. The output is always a native TOML datetime
// literal (offset datetime, local datetime, local date, or local time),
// never a quoted string.
func (d DateTime) MarshalTOML() ([]byte, error) {
	if d.Kind < OffsetDateTime || d.Kind > LocalTime {
		return nil, fmt.Errorf("toml: DateTime has invalid kind %d (zero value?)", d.Kind)
	}
	if d.Precision < 0 || d.Precision > 9 {
		return nil, fmt.Errorf("toml: DateTime.Precision is %d; must be between 0 and 9", d.Precision)
	}
	if d.Nanosecond < 0 || d.Nanosecond > 999999999 {
		return nil, fmt.Errorf("toml: DateTime.Nanosecond is %d; must be between 0 and 999999999", d.Nanosecond)
	}

	var b strings.Builder
	if d.Kind != LocalTime {
		if d.Month < 1 || d.Month > 12 || d.Day < 1 || d.Day > 31 {
			return nil, fmt.Errorf("toml: DateTime has invalid date %04d-%02d-%02d", d.Year, d.Month, d.Day)
		}
		fmt.Fprintf(&b, "%04d-%02d-%02d", d.Year, d.Month, d.Day)
	}
	if d.Kind == OffsetDateTime || d.Kind == LocalDateTime {
		b.WriteByte('T')
	}
	if d.Kind != LocalDate {
		if d.Hour > 23 || d.Minute > 59 || d.Second > 59 ||
			d.Hour < 0 || d.Minute < 0 || d.Second < 0 {
			return nil, fmt.Errorf("toml: DateTime has invalid time %02d:%02d:%02d", d.Hour, d.Minute, d.Second)
		}
		fmt.Fprintf(&b, "%02d:%02d:%02d", d.Hour, d.Minute, d.Second)
		if d.Precision > 0 {
			frac := fmt.Sprintf("%09d", d.Nanosecond)
			b.WriteByte('.')
			b.WriteString(frac[:d.Precision])
		}
	}
	if d.Kind == OffsetDateTime {
		off := d.Offset
		sign := '+'
		if off < 0 {
			sign = '-'
			off = -off
		}
		if off > 23*60+59 {
			return nil, fmt.Errorf("toml: DateTime.Offset is %d minutes; must be within +/-23:59", d.Offset)
		}
		fmt.Fprintf(&b, "%c%02d:%02d", sign, off/60, off%60)
	}
	return []byte(b.String()), nil
}

// dateTimeFromTime converts a time.Time to a DateTime, using the same
// location conventions as the default time.Time mapping. The precision is
// inferred as the minimum number of digits needed to represent the
// nanoseconds exactly.
func dateTimeFromTime(t time.Time) DateTime {
	var d DateTime
	switch t.Location() {
	case internal.LocalDatetime:
		d.Kind = LocalDateTime
	case internal.LocalDate:
		d.Kind = LocalDate
	case internal.LocalTime:
		d.Kind = LocalTime
	default:
		d.Kind = OffsetDateTime
		_, off := t.Zone()
		d.Offset = off / 60
	}
	if d.Kind != LocalTime {
		d.Year, d.Month, d.Day = t.Year(), int(t.Month()), t.Day()
	}
	if d.Kind != LocalDate {
		d.Hour, d.Minute, d.Second = t.Clock()
		d.Nanosecond = t.Nanosecond()
		// Infer precision: strip trailing zeros from the 9-digit fraction.
		p, n := 9, d.Nanosecond
		for p > 0 && n%10 == 0 {
			n /= 10
			p--
		}
		d.Precision = p
	}
	return d
}

// parseDateTimeLiteral parses a TOML datetime literal into a DateTime,
// preserving the offset and the exact number of fractional-second digits.
// Fractions with more than 9 digits are explicitly rejected instead of being
// silently truncated the way time.Parse does.
func parseDateTimeLiteral(s string) (DateTime, error) {
	var d DateTime
	s = datetimeRepl.Replace(s) // t→T, z→Z, space→T
	bad := func() error { return fmt.Errorf("toml: invalid TOML datetime %q", s) }

	// Optional date part: YYYY-MM-DD
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		if !isDigits(s[0:4]) || !isDigits(s[5:7]) || !isDigits(s[8:10]) {
			return d, bad()
		}
		d.Year, _ = strconv.Atoi(s[0:4])
		d.Month, _ = strconv.Atoi(s[5:7])
		d.Day, _ = strconv.Atoi(s[8:10])
		if d.Month < 1 || d.Month > 12 || d.Day < 1 || d.Day > 31 {
			return d, bad()
		}
		s = s[10:]
		if s == "" {
			d.Kind = LocalDate
			return d, nil
		}
		if s[0] != 'T' {
			return d, bad()
		}
		s = s[1:]
		d.Kind = LocalDateTime
	} else {
		d.Kind = LocalTime
	}

	// Time part: HH:MM:SS
	if len(s) < 8 || s[2] != ':' || s[5] != ':' ||
		!isDigits(s[0:2]) || !isDigits(s[3:5]) || !isDigits(s[6:8]) {
		return d, bad()
	}
	d.Hour, _ = strconv.Atoi(s[0:2])
	d.Minute, _ = strconv.Atoi(s[3:5])
	d.Second, _ = strconv.Atoi(s[6:8])
	if d.Hour > 23 || d.Minute > 59 || d.Second > 59 {
		return d, bad()
	}
	s = s[8:]

	// Optional fractional seconds.
	if len(s) > 0 && s[0] == '.' {
		i := 1
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		frac := s[1:i]
		if len(frac) == 0 {
			return d, bad()
		}
		if len(frac) > 9 {
			return d, fmt.Errorf("toml: fractional seconds in %q have %d digits; the maximum supported is 9", s, len(frac))
		}
		d.Nanosecond, _ = strconv.Atoi(frac + strings.Repeat("0", 9-len(frac)))
		d.Precision = len(frac)
		s = s[i:]
	}

	// Optional zone offset; only allowed on full datetimes.
	switch {
	case s == "":
		// Local type; nothing more to do.
	case d.Kind == LocalTime:
		return d, bad()
	case s == "Z":
		d.Kind = OffsetDateTime
	case len(s) == 6 && (s[0] == '+' || s[0] == '-') && s[3] == ':' &&
		isDigits(s[1:3]) && isDigits(s[4:6]):
		oh, _ := strconv.Atoi(s[1:3])
		om, _ := strconv.Atoi(s[4:6])
		if oh > 23 || om > 59 {
			return d, bad()
		}
		d.Offset = oh*60 + om
		if s[0] == '-' {
			d.Offset = -d.Offset
		}
		d.Kind = OffsetDateTime
	default:
		return d, bad()
	}
	return d, nil
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}
