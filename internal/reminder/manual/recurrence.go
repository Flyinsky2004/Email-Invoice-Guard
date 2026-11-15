package manual

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"email-invoice-guard/internal/core"
)

const dateLayout = "2006-01-02"

// parseDate converts a validated calendar date into a UTC midnight instant.
// Calendar arithmetic is done on these instants and never on wall-clock time,
// so a DST change can never shift a billing date.
func parseDate(d core.Date) (time.Time, error) {
	t, err := time.Parse(dateLayout, string(d))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: date %q", core.ErrInvalid, d)
	}
	return t, nil
}

func formatDate(t time.Time) core.Date { return core.Date(t.Format(dateLayout)) }

// daysInMonth returns the number of days in the given month.
func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// clampDay applies the v1 "last_day" policy: a billing day beyond the month's
// length becomes that month's last day.
func clampDay(year int, month time.Month, billingDay int) int {
	if last := daysInMonth(year, month); billingDay > last {
		return last
	}
	return billingDay
}

// billingDate is the clamped billing date for one calendar month.
func billingDate(year int, month time.Month, billingDay int) core.Date {
	day := clampDay(year, month, billingDay)
	return core.Date(time.Date(year, month, day, 0, 0, 0, 0, time.UTC).Format(dateLayout))
}

// firstBillingOnOrAfter returns the first billing date that is not before
// today. It is used when a plan is created or resumed, so a missed period is
// never materialized on the creation day.
func firstBillingOnOrAfter(today core.Date, billingDay int) (core.Date, error) {
	t, err := parseDate(today)
	if err != nil {
		return "", err
	}
	candidate := billingDate(t.Year(), t.Month(), billingDay)
	if candidate >= today {
		return candidate, nil
	}
	year, month := nextMonth(t.Year(), t.Month())
	return billingDate(year, month, billingDay), nil
}

// firstBillingAfter is the creation/resume rule from the feature contract: the
// billing day counts today only while the plan-local reminder time has not
// passed. At exactly the reminder instant today still counts, because the next
// tick is the reminder itself; one second later the period moves to next month.
func firstBillingAfter(now time.Time, billingDay, hour, minute int, loc *time.Location) (core.Date, error) {
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	today := core.LocalDate(now, loc)
	candidate, err := firstBillingOnOrAfter(today, billingDay)
	if err != nil {
		return "", err
	}
	if candidate != today {
		return candidate, nil
	}
	at := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if !local.After(at) {
		return today, nil
	}
	t, err := parseDate(today)
	if err != nil {
		return "", err
	}
	year, month := nextMonth(t.Year(), t.Month())
	return billingDate(year, month, billingDay), nil
}

// nextBillingDate advances one calendar month and re-applies the clamp. It
// never uses AddDate, because AddDate normalizes 31 January to 3 March and
// would skip a short month.
func nextBillingDate(due core.Date, billingDay int) (core.Date, error) {
	t, err := parseDate(due)
	if err != nil {
		return "", err
	}
	year, month := nextMonth(t.Year(), t.Month())
	return billingDate(year, month, billingDay), nil
}

func nextMonth(year int, month time.Month) (int, time.Month) {
	if month == time.December {
		return year + 1, time.January
	}
	return year, month + 1
}

// monthsBetween counts calendar month boundaries between two dates. It is the
// catch-up unit: 2026-08-15 to 2026-11-15 is three months.
func monthsBetween(from, to core.Date) (int, error) {
	a, err := parseDate(from)
	if err != nil {
		return 0, err
	}
	b, err := parseDate(to)
	if err != nil {
		return 0, err
	}
	return (b.Year()-a.Year())*12 + int(b.Month()-a.Month()), nil
}

// normalizeClock validates an HH:MM reminder time and returns its canonical
// form plus the hour and minute used for the plan-local comparison.
func normalizeClock(raw string) (string, int, int, error) {
	s := strings.TrimSpace(raw)
	parts := strings.Split(s, ":")
	if len(parts) != 2 || len(parts[0]) < 1 || len(parts[0]) > 2 || len(parts[1]) != 2 {
		return "", 0, 0, fmt.Errorf("%w: reminder time %q", core.ErrInvalid, raw)
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil {
		return "", 0, 0, fmt.Errorf("%w: reminder time %q", core.ErrInvalid, raw)
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, 0, fmt.Errorf("%w: reminder time %q", core.ErrInvalid, raw)
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return "", 0, 0, fmt.Errorf("%w: reminder time %q", core.ErrInvalid, raw)
	}
	return fmt.Sprintf("%02d:%02d", hour, minute), hour, minute, nil
}

// beforeClock reports whether the plan-local wall clock is earlier than the
// configured reminder time on that day.
func beforeClock(local time.Time, hour, minute int) bool {
	if local.Hour() != hour {
		return local.Hour() < hour
	}
	return local.Minute() < minute
}
