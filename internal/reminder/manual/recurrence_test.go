package manual

import (
	"errors"
	"testing"
	"time"

	"email-invoice-guard/internal/core"
)

func TestBillingDayWithinMonth(t *testing.T) {
	cases := []struct {
		year  int
		month time.Month
		day   int
		want  core.Date
	}{
		{2026, time.January, 1, "2026-01-01"},
		{2026, time.January, 15, "2026-01-15"},
		{2026, time.January, 28, "2026-01-28"},
		{2026, time.February, 1, "2026-02-01"},
		{2026, time.February, 15, "2026-02-15"},
		{2026, time.February, 28, "2026-02-28"},
		{2026, time.December, 28, "2026-12-28"},
	}
	for _, c := range cases {
		if got := billingDate(c.year, c.month, c.day); got != c.want {
			t.Errorf("billingDate(%d, %s, %d) = %s, want %s", c.year, c.month, c.day, got, c.want)
		}
	}
}

func TestBillingDayClampsToShortMonth(t *testing.T) {
	cases := []struct {
		year  int
		month time.Month
		day   int
		want  core.Date
	}{
		{2026, time.February, 29, "2026-02-28"},
		{2026, time.February, 30, "2026-02-28"},
		{2026, time.February, 31, "2026-02-28"},
		{2026, time.April, 31, "2026-04-30"},
		{2026, time.June, 31, "2026-06-30"},
		{2026, time.September, 31, "2026-09-30"},
		{2026, time.November, 31, "2026-11-30"},
		{2028, time.February, 29, "2028-02-29"},
		{2028, time.February, 31, "2028-02-29"},
		{2026, time.January, 31, "2026-01-31"},
	}
	for _, c := range cases {
		if got := billingDate(c.year, c.month, c.day); got != c.want {
			t.Errorf("billingDate(%d, %s, %d) = %s, want %s", c.year, c.month, c.day, got, c.want)
		}
	}
}

func TestFirstBillingOnOrAfter(t *testing.T) {
	cases := []struct {
		today core.Date
		day   int
		want  core.Date
	}{
		{"2026-01-10", 15, "2026-01-15"},
		{"2026-01-15", 15, "2026-01-15"},
		{"2026-01-16", 15, "2026-02-15"},
		{"2026-01-31", 31, "2026-01-31"},
		{"2026-02-01", 31, "2026-02-28"},
		{"2026-02-28", 31, "2026-02-28"},
		{"2026-03-01", 31, "2026-03-31"},
		{"2026-12-20", 15, "2027-01-15"},
		{"2028-02-01", 29, "2028-02-29"},
	}
	for _, c := range cases {
		got, err := firstBillingOnOrAfter(c.today, c.day)
		if err != nil || got != c.want {
			t.Errorf("firstBillingOnOrAfter(%s, %d) = %s, %v; want %s", c.today, c.day, got, err, c.want)
		}
	}
	if _, err := firstBillingOnOrAfter("not-a-date", 15); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("invalid date error = %v", err)
	}
}

func TestNextBillingDate(t *testing.T) {
	cases := []struct {
		due  core.Date
		day  int
		want core.Date
	}{
		{"2026-01-31", 31, "2026-02-28"},
		{"2026-02-28", 31, "2026-03-31"},
		{"2026-04-30", 31, "2026-05-31"},
		{"2026-12-15", 15, "2027-01-15"},
		{"2028-02-29", 29, "2028-03-29"},
		{"2026-11-15", 15, "2026-12-15"},
	}
	for _, c := range cases {
		got, err := nextBillingDate(c.due, c.day)
		if err != nil || got != c.want {
			t.Errorf("nextBillingDate(%s, %d) = %s, %v; want %s", c.due, c.day, got, err, c.want)
		}
	}
}

func TestMonthsBetween(t *testing.T) {
	cases := []struct {
		from, to core.Date
		want     int
	}{
		{"2026-08-15", "2026-11-15", 3},
		{"2026-11-01", "2026-11-30", 0},
		{"2025-12-31", "2026-01-01", 1},
		{"2026-11-15", "2026-08-15", -3},
		{"2026-01-31", "2027-01-31", 12},
	}
	for _, c := range cases {
		got, err := monthsBetween(c.from, c.to)
		if err != nil || got != c.want {
			t.Errorf("monthsBetween(%s, %s) = %d, %v; want %d", c.from, c.to, got, err, c.want)
		}
	}
}

func TestNormalizeClock(t *testing.T) {
	valid := []struct {
		raw          string
		want         string
		hour, minute int
	}{
		{"09:00", "09:00", 9, 0},
		{"9:05", "09:05", 9, 5},
		{"23:59", "23:59", 23, 59},
		{"00:00", "00:00", 0, 0},
	}
	for _, c := range valid {
		got, hour, minute, err := normalizeClock(c.raw)
		if err != nil || got != c.want || hour != c.hour || minute != c.minute {
			t.Errorf("normalizeClock(%q) = %q, %d, %d, %v", c.raw, got, hour, minute, err)
		}
	}
	invalid := []string{"", "24:00", "12:60", "12:5", "aa:bb", "12", "12:00:00", "-1:00"}
	for _, raw := range invalid {
		if _, _, _, err := normalizeClock(raw); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("normalizeClock(%q) error = %v, want ErrInvalid", raw, err)
		}
	}
}

func TestBeforeClock(t *testing.T) {
	at := func(hour, minute int) time.Time {
		return time.Date(2026, 11, 15, hour, minute, 0, 0, time.UTC)
	}
	if !beforeClock(at(8, 59), 9, 0) {
		t.Fatal("08:59 must be before 09:00")
	}
	if beforeClock(at(9, 0), 9, 0) {
		t.Fatal("09:00 is not before 09:00")
	}
	if beforeClock(at(9, 1), 9, 0) {
		t.Fatal("09:01 is not before 09:00")
	}
	if beforeClock(at(10, 0), 9, 0) {
		t.Fatal("10:00 is not before 09:00")
	}
}
