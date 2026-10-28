package job

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// TestDecideBacksOff pins the backoff: a job failing the same way every 5
// minutes for a day alerts at once, then 15m, 30m, 1h, 2h and 4h after the
// previous alert, then every 6h: 8 alerts instead of 288.
func TestDecideBacksOff(t *testing.T) {
	var f *Failing
	var at []time.Duration
	for i := range 24 * 12 {
		now := t0.Add(time.Duration(i) * 5 * time.Minute)
		var a Alert
		a, f = decide(f, false, "same", now)
		switch a {
		case AlertFailure, AlertRepeat:
			at = append(at, now.Sub(t0))
		case AlertSuppressed:
		default:
			t.Fatalf("run %d: alert %s", i, a)
		}
	}
	want := []time.Duration{0, 15 * time.Minute, 45 * time.Minute, 105 * time.Minute, 225 * time.Minute, 465 * time.Minute, 825 * time.Minute, 1185 * time.Minute}
	if len(at) != len(want) {
		t.Fatalf("alerts at %v, want %v", at, want)
	}
	for i := range want {
		if at[i] != want[i] {
			t.Fatalf("alerts at %v, want %v", at, want)
		}
	}
	if f.Failures != 288 || f.Since != t0 {
		t.Errorf("failing %+v, want 288 failures since %s", f, t0)
	}
}

// TestDecideNewFailureAndRecovery: a different failure alerts at once and
// keeps the failing streak; the first success recovers and clears it.
func TestDecideNewFailureAndRecovery(t *testing.T) {
	a, f := decide(nil, false, "a", t0)
	if a != AlertFailure {
		t.Fatalf("first failure: %s", a)
	}
	if a, f = decide(f, false, "a", t0.Add(time.Minute)); a != AlertSuppressed || f.Unalerted != 1 {
		t.Fatalf("repeat within backoff: %s %+v", a, f)
	}
	if a, f = decide(f, false, "b", t0.Add(2*time.Minute)); a != AlertFailure || f.Since != t0 || f.Failures != 3 || f.Alerts != 1 {
		t.Fatalf("new failure: %s %+v", a, f)
	}
	if a, f = decide(f, true, "", t0.Add(3*time.Minute)); a != AlertRecovery || f != nil {
		t.Fatalf("recovery: %s %+v", a, f)
	}
	if a, f = decide(f, true, "", t0.Add(4*time.Minute)); a != AlertNone || f != nil {
		t.Fatalf("ok after ok: %s %+v", a, f)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"error at 12:03:44 pid 4411\n\n  timeout": "error at #:#:# pid # timeout",
		"commit deadbeefcafe0 failed":             "commit # failed",
		"added a bad face":                        "added a bad face",
		"0x1f":                                    "#x#",
	} {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
