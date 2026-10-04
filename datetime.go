package toml

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml/internal"
)

// DateTimeKind identifies which of the four TOML date/time types a DateTime
// value represents.
type DateTimeKind uint8

const (
	// OffsetDateTime is a TOML offset date-time, e.g. 2020-01-02T03:04:05+08:00.
	// The offset is preserved and the value represents an unambiguous instant.
	OffsetDateTime DateTimeKind = iota

	// LocalDateTime is a TOML local date-time, e.g. 2020-01-02T03:04:05. It
	// has no timezone and is not tied to any particular instant.
	LocalDateTime

	// LocalDate is a TOML local date, e.g. 2020-01-02.
	LocalDate

	// LocalTime is a TOML local time, e.g. 03:04:05. It has no date and no
	// timezone.
	LocalTime
)

// DateTime is an explicit carrier for TOML date/time values that preserves the
// exact TOML representation: whether the value carried an offset, the offset
// itself, and the fractional-second precision (including trailing zeros).
//
// Unlike the default time.Time mapping, a DateTime round-trips through Decode
// and Encode without attaching the machine's local timezone to timezone-less
// values and without truncating fractional seconds. Use it as a struct field
// type:
//
//	type doc struct {
//	    When toml.DateTime
//	}
//
// The zero value is a valid offset date-time (0001-01-01T00:00:00Z).
type DateTime struct {
	Kind       DateTimeKind
	Year       int
	Month      int
	Day        int
	Hour       int
	Minute     int
	Second     int
	Nanosecond int
	// Precision is the number of fractional-second digits present in the
	// original TOML text (0 to 9). It is retained so that trailing zeros are
	// preserved on round-trip.
	Precision int
	// Offset is the offset from UTC in seconds. It is only meaningful for
	// OffsetDateTime values.
	Offset int
}

// ParseDateTime parses a TOML date/time literal.
//
// The four TOML date/time types are distinguished by their shape:
//
//	2020-01-02T03:04:05+08:00  -> OffsetDateTime
//	2020-01-02T03:04:05        -> LocalDateTime
//	2020-01-02                 -> LocalDate
//	03:04:05                   -> LocalTime
//
// Fractional seconds may have 0 to 9 digits; more than 9 digits are rejected
// with an error instead of being silently truncated.
func ParseDateTime(s string) (DateTime, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DateTime{}, errors.New("toml: empty date-time")
	}

	// Split the calendar date from the time-of-day on the T/t/space
	// separator. Local times contain no separator, so they fall through to the
	// time-only branch below.
	datePart, timePart := s, ""
	if i := strings.IndexAny(s, "Tt "); i >= 0 {
		datePart = s[:i]
		timePart = s[i+1:]
	}

	var dt DateTime
	switch {
	case timePart != "":
		// Offset date-time or local date-time.
		y, m, d, err := parseDate(datePart)
		if err != nil {
			return DateTime{}, err
		}
		h, mi, se, ns, prec, rest, err := parseTime(timePart)
		if err != nil {
			return DateTime{}, err
		}
		dt = DateTime{
			Year: y, Month: m, Day: d,
			Hour: h, Minute: mi, Second: se,
			Nanosecond: ns, Precision: prec,
		}
		if rest != "" {
			off, err := parseOffset(rest)
			if err != nil {
				return DateTime{}, err
			}
			dt.Kind = OffsetDateTime
			dt.Offset = off
		} else {
			dt.Kind = LocalDateTime
		}
	case len(datePart) == len("2006-01-02") && datePart[4] == '-' && datePart[7] == '-':
		// Local date.
		y, m, d, err := parseDate(datePart)
		if err != nil {
			return DateTime{}, err
		}
		dt = DateTime{Kind: LocalDate, Year: y, Month: m, Day: d}
	case strings.Contains(datePart, ":"):
		// Local time.
		h, mi, se, ns, prec, rest, err := parseTime(datePart)
		if err != nil {
			return DateTime{}, err
		}
		if rest != "" {
			return DateTime{}, fmt.Errorf("toml: local time %q must not have an offset", s)
		}
		dt = DateTime{
			Kind: LocalTime, Hour: h, Minute: mi, Second: se,
			Nanosecond: ns, Precision: prec,
		}
	default:
		return DateTime{}, fmt.Errorf("toml: invalid date-time %q", s)
	}
	return dt, nil
}

// parseDate parses a YYYY-MM-DD date and validates the ranges.
func parseDate(s string) (year, month, day int, err error) {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return 0, 0, 0, fmt.Errorf("toml: invalid date %q", s)
	}
	if year, err = atoi2(s[0:4]); err != nil {
		return 0, 0, 0, fmt.Errorf("toml: invalid year %q: %w", s[0:4], err)
	}
	if month, err = atoi2(s[5:7]); err != nil {
		return 0, 0, 0, fmt.Errorf("toml: invalid month %q: %w", s[5:7], err)
	}
	if day, err = atoi2(s[8:10]); err != nil {
		return 0, 0, 0, fmt.Errorf("toml: invalid day %q: %w", s[8:10], err)
	}
	if month < 1 || month > 12 {
		return 0, 0, 0, fmt.Errorf("toml: invalid month %q", s)
	}
	maxDay := daysInMonth(year, month)
	if day < 1 || day > maxDay {
		return 0, 0, 0, fmt.Errorf("toml: invalid day %q", s)
	}
	return year, month, day, nil
}

// parseTime parses a HH:MM:SS[.frac] time-of-day. It returns any trailing text
// (a time offset) separately. Fractional seconds are limited to 9 digits.
func parseTime(s string) (hour, minute, second, nanosecond, precision int, rest string, err error) {
	if len(s) < 8 || s[2] != ':' || s[5] != ':' {
		return 0, 0, 0, 0, 0, "", fmt.Errorf("toml: invalid time %q", s)
	}
	if hour, err = atoi2(s[0:2]); err != nil {
		return 0, 0, 0, 0, 0, "", fmt.Errorf("toml: invalid hour %q: %w", s[0:2], err)
	}
	if minute, err = atoi2(s[3:5]); err != nil {
		return 0, 0, 0, 0, 0, "", fmt.Errorf("toml: invalid minute %q: %w", s[3:5], err)
	}
	if second, err = atoi2(s[6:8]); err != nil {
		return 0, 0, 0, 0, 0, "", fmt.Errorf("toml: invalid second %q: %w", s[6:8], err)
	}
	if hour > 23 || minute > 59 || second > 59 {
		return 0, 0, 0, 0, 0, "", fmt.Errorf("toml: invalid time %q", s)
	}
	rest = s[8:]
	if strings.HasPrefix(rest, ".") {
		frac := rest[1:]
		n := 0
		for n < len(frac) && frac[n] >= '0' && frac[n] <= '9' {
			n++
		}
		if n == 0 {
			return 0, 0, 0, 0, 0, "", fmt.Errorf("toml: invalid time %q: '.' must be followed by digits", s)
		}
		if n > 9 {
			return 0, 0, 0, 0, 0, "", fmt.Errorf("toml: invalid time %q: fractional seconds have %d digits; at most 9 are supported", s, n)
		}
		precision = n
		nanosecond, err = strconv.Atoi(frac[:n] + strings.Repeat("0", 9-n))
		if err != nil {
			return 0, 0, 0, 0, 0, "", fmt.Errorf("toml: invalid fractional seconds %q: %w", frac[:n], err)
		}
		rest = frac[n:]
	}
	return hour, minute, second, nanosecond, precision, rest, nil
}

// parseOffset parses a time offset: Z/z or ±HH:MM.
func parseOffset(s string) (offset int, err error) {
	if s == "Z" || s == "z" {
		return 0, nil
	}
	if len(s) != 6 || (s[0] != '+' && s[0] != '-') || s[3] != ':' {
		return 0, fmt.Errorf("toml: invalid time offset %q", s)
	}
	oh, err := atoi2(s[1:3])
	if err != nil {
		return 0, fmt.Errorf("toml: invalid time offset %q: %w", s, err)
	}
	om, err := atoi2(s[4:6])
	if err != nil {
		return 0, fmt.Errorf("toml: invalid time offset %q: %w", s, err)
	}
	if oh > 23 || om > 59 {
		return 0, fmt.Errorf("toml: invalid time offset %q", s)
	}
	offset = oh*3600 + om*60
	if s[0] == '-' {
		offset = -offset
	}
	return offset, nil
}

func atoi2(s string) (int, error) {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("expected digits, got %q", s)
		}
	}
	return strconv.Atoi(s)
}

func isLeap(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

func daysInMonth(year, month int) int {
	switch month {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	case 2:
		if isLeap(year) {
			return 29
		}
		return 28
	}
	return 0
}

// String returns the TOML literal for the value. It is the exact text that
// will be encoded, e.g. 2020-01-02T03:04:05+08:00, 2020-01-02T03:04:05,
// 2020-01-02, or 03:04:05.120.
func (dt DateTime) String() string {
	var b strings.Builder
	switch dt.Kind {
	case OffsetDateTime, LocalDateTime:
		fmt.Fprintf(&b, "%04d-%02d-%02dT%02d:%02d:%02d",
			dt.Year, dt.Month, dt.Day, dt.Hour, dt.Minute, dt.Second)
		b.WriteString(fmtFraction(dt.Nanosecond, dt.Precision))
		if dt.Kind == OffsetDateTime {
			b.WriteString(fmtOffset(dt.Offset))
		}
	case LocalDate:
		fmt.Fprintf(&b, "%04d-%02d-%02d", dt.Year, dt.Month, dt.Day)
	case LocalTime:
		fmt.Fprintf(&b, "%02d:%02d:%02d", dt.Hour, dt.Minute, dt.Second)
		b.WriteString(fmtFraction(dt.Nanosecond, dt.Precision))
	}
	return b.String()
}

// MarshalTOML implements Marshaler. The value is written as a TOML native
// date/time literal, not a quoted string.
func (dt DateTime) MarshalTOML() ([]byte, error) {
	return []byte(dt.String()), nil
}

// UnmarshalTOML implements Unmarshaler. It accepts the parsed TOML date-time
// value (which retains the original literal), a raw string, or a time.Time.
func (dt *DateTime) UnmarshalTOML(data any) error {
	switch v := data.(type) {
	case internal.DatetimeValue:
		parsed, err := ParseDateTime(v.Raw)
		if err != nil {
			return err
		}
		*dt = parsed
	case string:
		parsed, err := ParseDateTime(v)
		if err != nil {
			return err
		}
		*dt = parsed
	case time.Time:
		*dt = FromTime(v)
	default:
		return fmt.Errorf("toml: cannot unmarshal %T into DateTime", data)
	}
	return nil
}

// Time returns the value as a time.Time.
//
// OffsetDateTime values are returned as an unambiguous instant with their
// original offset. Local values (LocalDateTime, LocalDate, LocalTime) have no
// timezone, so the wall-clock fields are returned in UTC; the machine's local
// timezone is never applied.
func (dt DateTime) Time() time.Time {
	loc := time.UTC
	if dt.Kind == OffsetDateTime {
		loc = time.FixedZone("", dt.Offset)
	}
	return time.Date(dt.Year, time.Month(dt.Month), dt.Day,
		dt.Hour, dt.Minute, dt.Second, dt.Nanosecond, loc)
}

// FromTime converts a time.Time to a DateTime, preserving its offset and
// nanosecond precision. The result is always an OffsetDateTime.
func FromTime(t time.Time) DateTime {
	_, off := t.Zone()
	ns := t.Nanosecond()
	prec := 0
	if ns != 0 {
		prec = 9
		for prec > 1 && ns%10 == 0 {
			ns /= 10
			prec--
		}
	}
	return DateTime{
		Kind: OffsetDateTime,
		Year: t.Year(), Month: int(t.Month()), Day: t.Day(),
		Hour: t.Hour(), Minute: t.Minute(), Second: t.Second(),
		Nanosecond: t.Nanosecond(), Precision: prec, Offset: off,
	}
}

// fmtFraction formats the fractional second with exactly precision digits,
// retaining trailing zeros.
func fmtFraction(nanosecond, precision int) string {
	if precision == 0 {
		return ""
	}
	digits := fmt.Sprintf("%09d", nanosecond)
	return "." + digits[:precision]
}

// fmtOffset formats a UTC offset as Z (for UTC) or ±HH:MM.
func fmtOffset(offset int) string {
	if offset == 0 {
		return "Z"
	}
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	return fmt.Sprintf("%s%02d:%02d", sign, offset/3600, (offset%3600)/60)
}
