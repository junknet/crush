package tools

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronSpec is a parsed standard 5-field cron expression:
//
//	minute hour day-of-month month day-of-week
//
// Each field is a set of permitted integer values. day-of-week 0 and 7 both
// mean Sunday (Vixie-cron convention).
//
// The implementation is deliberately small: it covers `*`, `*/N`, `A-B`, lists
// with commas, and bare integers — enough for the wake-up use case without
// dragging in a third-party dependency.
type cronSpec struct {
	minute    map[int]bool
	hour      map[int]bool
	day       map[int]bool
	month     map[int]bool
	dayOfWeek map[int]bool
}

// parseCronExpression returns a cronSpec ready for cronSpec.next, or an error
// describing which field rejected the input.
func parseCronExpression(expr string) (*cronSpec, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron: expected 5 space-separated fields, got %d (%q)", len(fields), expr)
	}
	minute, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("cron minute: %w", err)
	}
	hour, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("cron hour: %w", err)
	}
	day, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("cron day-of-month: %w", err)
	}
	month, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("cron month: %w", err)
	}
	dow, err := parseCronField(fields[4], 0, 7)
	if err != nil {
		return nil, fmt.Errorf("cron day-of-week: %w", err)
	}
	// Normalise: 7 == Sunday == 0.
	if dow[7] {
		dow[0] = true
		delete(dow, 7)
	}
	return &cronSpec{
		minute:    minute,
		hour:      hour,
		day:       day,
		month:     month,
		dayOfWeek: dow,
	}, nil
}

func parseCronField(field string, lo, hi int) (map[int]bool, error) {
	out := make(map[int]bool)
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty entry in %q", field)
		}
		step := 1
		// "*/N" or "A-B/N"
		if slash := strings.Index(part, "/"); slash != -1 {
			s, err := strconv.Atoi(part[slash+1:])
			if err != nil || s <= 0 {
				return nil, fmt.Errorf("bad step %q", part)
			}
			step = s
			part = part[:slash]
		}
		var first, last int
		switch {
		case part == "*":
			first, last = lo, hi
		case strings.Contains(part, "-"):
			pieces := strings.SplitN(part, "-", 2)
			a, errA := strconv.Atoi(pieces[0])
			b, errB := strconv.Atoi(pieces[1])
			if errA != nil || errB != nil {
				return nil, fmt.Errorf("bad range %q", part)
			}
			first, last = a, b
		default:
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("bad value %q", part)
			}
			first, last = n, n
		}
		if first < lo || last > hi || first > last {
			return nil, fmt.Errorf("value %q out of range [%d..%d]", part, lo, hi)
		}
		for v := first; v <= last; v += step {
			out[v] = true
		}
	}
	return out, nil
}

// next returns the smallest time strictly after `from` (in `from`'s location)
// that satisfies the cron spec. The search is bounded so a pathological
// expression cannot loop forever.
func (cs *cronSpec) next(from time.Time) (time.Time, error) {
	// Truncate to minute precision then advance one minute so we always
	// return a time strictly after `from`.
	t := from.Truncate(time.Minute).Add(time.Minute)

	// 4 years is enough to cover any valid 5-field cron expression that
	// will ever match (Feb 29 on a leap year is the tightest practical
	// boundary). If we don't match by then the expression is unsatisfiable.
	deadline := t.Add(4 * 365 * 24 * time.Hour)

	for t.Before(deadline) {
		if !cs.month[int(t.Month())] {
			// Jump to the first of the next month.
			year, month := t.Year(), t.Month()+1
			if month > 12 {
				year++
				month = 1
			}
			t = time.Date(year, month, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		dow := int(t.Weekday()) // Sunday = 0..Saturday = 6.
		if !cs.day[t.Day()] || !cs.dayOfWeek[dow] {
			next := t.AddDate(0, 0, 1)
			t = time.Date(next.Year(), next.Month(), next.Day(), 0, 0, 0, 0, t.Location())
			continue
		}
		if !cs.hour[t.Hour()] {
			next := t.Add(time.Hour)
			t = time.Date(next.Year(), next.Month(), next.Day(), next.Hour(), 0, 0, 0, t.Location())
			continue
		}
		if !cs.minute[t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cron: expression never matches within 4 years from %s", from.Format(time.RFC3339))
}
