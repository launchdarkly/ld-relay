package ldtime

import "time"

// UnixMillisecondTime is a millisecond timestamp starting from the Unix epoch.
type UnixMillisecondTime uint64

// UnixMillisFromTime converts a Time value into UnixMillisecondTime.
func UnixMillisFromTime(t time.Time) UnixMillisecondTime {
	ms := time.Duration(t.UnixNano()) / time.Millisecond
	return UnixMillisecondTime(ms)
}

// UnixMillisNow returns the current date/time as a UnixMillisecondTime.
func UnixMillisNow() UnixMillisecondTime {
	return UnixMillisFromTime(time.Now())
}
