package internal

import "time"

// DatetimeValue is the parsed form of a TOML date-time primitive.
//
// It retains the original literal (Raw) so that types which want to validate or
// format the value themselves (e.g. the public DateTime carrier) can do so
// without losing fractional-second precision, along with the time.Time (T)
// used by the default time.Time mapping.
type DatetimeValue struct {
	Raw string
	T   time.Time
}
