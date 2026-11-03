package job

import (
	"strings"
	"testing"
	"time"
)

func TestCronNext(t *testing.T) {
	at := func(s string) time.Time {
		t.Helper()
		v, err := time.ParseInLocation("2006-01-02 15:04", s, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	// 2026-11-03 is a Tuesday.
	cases := []struct{ schedule, after, want string }{
		{"* * * * *", "2026-11-03 10:00", "2026-11-03 10:01"},
		{"*/5 * * * *", "2026-11-03 10:02", "2026-11-03 10:05"},
		{"30 23 * * *", "2026-11-03 23:30", "2026-11-04 23:30"},
		{"0 9 * * 1-5", "2026-11-06 09:00", "2026-11-09 09:00"}, // Friday to Monday
		{"0 9 * * mon,FRI", "2026-11-03 09:00", "2026-11-06 09:00"},
		{"0 0 * * 7", "2026-11-03 00:00", "2026-11-08 00:00"}, // 7 is Sunday
		{"0 0 1 jan *", "2026-11-03 00:00", "2027-01-01 00:00"},
		{"0 0 29 2 *", "2026-11-03 00:00", "2028-02-29 00:00"},
		{"10-20/5 8 * * *", "2026-11-03 08:15", "2026-11-03 08:20"},
		{"5/20 * * * *", "2026-11-03 08:45", "2026-11-03 09:05"},
		// Both day fields restricted: either matches, as cron has it.
		{"0 0 13 * 5", "2026-11-03 00:00", "2026-11-06 00:00"},
		{"0 0 13 * 5", "2026-11-12 00:00", "2026-11-13 00:00"},
		// One of them *: both must match.
		{"0 0 13 * *", "2026-11-03 00:00", "2026-11-13 00:00"},
		{"0 0 */2 * 5", "2026-11-03 00:00", "2026-11-13 00:00"},
		{"0 0 30 2 *", "2026-11-03 00:00", ""},
	}
	for _, tc := range cases {
		c, err := parseCron(tc.schedule)
		if err != nil {
			t.Fatalf("%s: %v", tc.schedule, err)
		}
		got := c.next(at(tc.after))
		if tc.want == "" {
			if !got.IsZero() {
				t.Errorf("%s after %s = %s, want never", tc.schedule, tc.after, got)
			}
			continue
		}
		if !got.Equal(at(tc.want)) {
			t.Errorf("%s after %s = %s, want %s", tc.schedule, tc.after, got.Format("2006-01-02 15:04"), tc.want)
		}
	}
}

// TestCronNextDST: a local schedule keeps its local time across a clock
// change, and an hour the change skips is not waited for.
func TestCronNextDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	c, _ := parseCron("30 9 * * *")
	got := c.next(time.Date(2026, 10, 31, 9, 30, 0, 0, ny))
	if want := time.Date(2026, 11, 1, 9, 30, 0, 0, ny); !got.Equal(want) {
		t.Errorf("across the fall change: %s, want %s", got, want)
	}
	c, _ = parseCron("30 2 * * *")
	got = c.next(time.Date(2026, 3, 8, 1, 0, 0, 0, ny))
	if got.IsZero() || got.Before(time.Date(2026, 3, 8, 1, 0, 0, 0, ny)) {
		t.Errorf("across the spring change: %s", got)
	}
}

func TestParseCronRefuses(t *testing.T) {
	for schedule, want := range map[string]string{
		"@daily":         "not 5 cron fields",
		"60 * * * *":     "minute",
		"* 24 * * *":     "hour",
		"* * 0 * *":      "day of month",
		"* * * 13 *":     "month",
		"* * * * 8":      "day of week",
		"5-1 * * * *":    "ends before",
		"*/0 * * * *":    "step",
		"* * * smarch *": "month",
		"1,,2 * * * *":   "minute",
	} {
		if _, err := parseCron(schedule); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", schedule, err, want)
		}
	}
}
