package job

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cron is a parsed 5-field schedule, as cron reads it: each field a set of
// the values it matches.
type cron struct {
	minute, hour, dom, month, dow uint64
	// domStar and dowStar: the field starts with *. When neither does, a day
	// matches if either matches, as cron has it.
	domStar, dowStar bool
}

var (
	monthNames = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	dowNames   = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
)

// parseCron parses a schedule of 5 fields: minute, hour, day of month, month
// and day of week. A field is a list of *, N or N-M, each with an optional
// /STEP; months and days of week may be named (jan, mon); day of week 7 is
// Sunday.
func parseCron(s string) (cron, error) {
	f := strings.Fields(s)
	if len(f) != 5 {
		return cron{}, fmt.Errorf("not 5 cron fields")
	}
	var c cron
	var err error
	for _, x := range []struct {
		p        *uint64
		field    string
		what     string
		min, max int
		names    []string
	}{
		{&c.minute, f[0], "minute", 0, 59, nil},
		{&c.hour, f[1], "hour", 0, 23, nil},
		{&c.dom, f[2], "day of month", 1, 31, nil},
		{&c.month, f[3], "month", 1, 12, monthNames},
		{&c.dow, f[4], "day of week", 0, 7, dowNames},
	} {
		if *x.p, err = parseField(x.field, x.min, x.max, x.names); err != nil {
			return cron{}, fmt.Errorf("%s %q: %v", x.what, x.field, err)
		}
	}
	if c.dow&(1<<7) != 0 {
		c.dow |= 1
	}
	c.domStar, c.dowStar = strings.HasPrefix(f[2], "*"), strings.HasPrefix(f[4], "*")
	return c, nil
}

func parseField(field string, min, max int, names []string) (uint64, error) {
	var set uint64
	for _, item := range strings.Split(field, ",") {
		rng, step, stepped := strings.Cut(item, "/")
		lo, hi := min, max
		if rng != "*" {
			a, b, isRange := strings.Cut(rng, "-")
			var err error
			if lo, err = value(a, min, max, names); err != nil {
				return 0, err
			}
			hi = lo
			if isRange {
				if hi, err = value(b, min, max, names); err != nil {
					return 0, err
				}
				if hi < lo {
					return 0, fmt.Errorf("range %s ends before it starts", rng)
				}
			} else if stepped {
				hi = max // N/STEP: from N to the end, as cron has it
			}
		}
		by := 1
		if stepped {
			n, err := strconv.Atoi(step)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("step %q is not a positive number", step)
			}
			by = n
		}
		for v := lo; v <= hi; v += by {
			set |= 1 << v
		}
	}
	return set, nil
}

func value(s string, min, max int, names []string) (int, error) {
	for i, name := range names {
		if strings.EqualFold(s, name) {
			return i + min, nil
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%q is not a value from %d to %d", s, min, max)
	}
	return n, nil
}

func (c cron) day(t time.Time) bool {
	dom, dow := c.dom&(1<<t.Day()) != 0, c.dow&(1<<int(t.Weekday())) != 0
	if c.domStar || c.dowStar {
		return dom && dow
	}
	return dom || dow
}

// next returns the first minute after t that c matches, in t's location, or
// the zero time if none comes within 5 years (such as 30 2 31 2 *).
func (c cron) next(t time.Time) time.Time {
	loc := t.Location()
	t = t.Truncate(time.Minute).Add(time.Minute)
	end := t.AddDate(5, 0, 0)
	for t.Before(end) {
		y, mo, d := t.Date()
		var n time.Time
		switch {
		case c.month&(1<<int(mo)) == 0:
			n = time.Date(y, mo+1, 1, 0, 0, 0, 0, loc)
		case !c.day(t):
			n = time.Date(y, mo, d+1, 0, 0, 0, 0, loc)
		case c.hour&(1<<t.Hour()) == 0:
			n = time.Date(y, mo, d, t.Hour()+1, 0, 0, 0, loc)
		case c.minute&(1<<t.Minute()) == 0:
			n = t.Add(time.Minute)
		default:
			return t
		}
		if !n.After(t) { // a clock change led back
			n = t.Add(time.Minute)
		}
		t = n
	}
	return time.Time{}
}
