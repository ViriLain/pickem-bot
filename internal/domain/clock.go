package domain

import "time"

// Clock supplies the current time so tests can control it.
type Clock interface {
	Now() time.Time
}

// SystemClock is the real clock, in UTC.
type SystemClock struct{}

// Now returns the current UTC time.
func (SystemClock) Now() time.Time { return time.Now().UTC() }
