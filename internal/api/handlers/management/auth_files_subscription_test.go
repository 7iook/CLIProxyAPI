package management

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func mustSubDate(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(subscriptionDateLayout, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

// cstNoon returns noon Asia/Shanghai on the given date (safe from day-boundary effects).
func cstNoon(t *testing.T, value string) time.Time {
	t.Helper()
	d := mustSubDate(t, value)
	return time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, subscriptionLocation)
}

func TestAddMonthsClamped(t *testing.T) {
	cases := []struct {
		anchor string
		months int
		want   string
	}{
		{"2026-01-31", 1, "2026-02-28"},
		{"2026-01-31", 2, "2026-03-31"},
		{"2026-01-31", 3, "2026-04-30"},
		{"2028-01-31", 1, "2028-02-29"}, // leap year
		{"2026-03-31", 1, "2026-04-30"},
		{"2026-09-02", 1, "2026-10-02"},
		{"2026-11-30", 2, "2027-01-30"}, // year rollover
		{"2026-12-15", 1, "2027-01-15"},
		{"2028-02-29", 12, "2029-02-28"}, // annual from leap day
		{"2028-02-29", 48, "2032-02-29"},
		{"2026-05-31", 0, "2026-05-31"},
		{"2026-03-31", -1, "2026-02-28"},
	}
	for _, tc := range cases {
		got := addMonthsClamped(mustSubDate(t, tc.anchor), tc.months).Format(subscriptionDateLayout)
		if got != tc.want {
			t.Errorf("addMonthsClamped(%s, %d) = %s, want %s", tc.anchor, tc.months, got, tc.want)
		}
	}
}

// cstAt returns the given CST wall-clock minute ("YYYY-MM-DD HH:mm").
func cstAt(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation(subscriptionMinuteLayout, value, subscriptionLocation)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

func TestAddMonthsClampedKeepsTimeOfDay(t *testing.T) {
	got := addMonthsClamped(cstAt(t, "2026-01-31 23:45"), 1)
	if s := got.Format(subscriptionMinuteLayout); s != "2026-02-28 23:45" || got.Location() != subscriptionLocation {
		t.Fatalf("addMonthsClamped = %s (%v), want 2026-02-28 23:45 CST", s, got.Location())
	}
}

func TestComputeSubscriptionPeriod(t *testing.T) {
	day := func(v string) time.Time { return cstAt(t, v+" 00:00") }
	at := func(v string) *time.Time { d := cstAt(t, v); return &d }
	cases := []struct {
		name      string
		sub       authFileSubscription
		now       time.Time
		wantStart string
		wantEnd   string
		wantDays  int
		wantSecs  int64 // compared only when checkSecs is set
		checkSecs bool
		wantState string
	}{
		{
			name:      "single monthly period active",
			sub:       authFileSubscription{Start: day("2026-11-03"), Cycle: subscriptionCycleMonthly},
			now:       cstNoon(t, "2026-11-03"),
			wantStart: "2026-11-03 00:00", wantEnd: "2026-12-03 00:00", wantDays: 29, wantState: subscriptionStateActive,
		},
		{
			name:      "warning inside 7 days",
			sub:       authFileSubscription{Start: day("2026-11-03"), Cycle: subscriptionCycleMonthly},
			now:       cstNoon(t, "2026-11-26"),
			wantStart: "2026-11-03 00:00", wantEnd: "2026-12-03 00:00", wantDays: 6, wantState: subscriptionStateWarning,
		},
		{
			name:      "exactly 7 days left is warning",
			sub:       authFileSubscription{Start: day("2026-11-03"), Cycle: subscriptionCycleMonthly},
			now:       cstAt(t, "2026-11-26 00:00"),
			wantStart: "2026-11-03 00:00", wantEnd: "2026-12-03 00:00", wantDays: 7, wantSecs: 7 * 86400, checkSecs: true, wantState: subscriptionStateWarning,
		},
		{
			name:      "7.5 days left is still active",
			sub:       authFileSubscription{Start: day("2026-11-03"), Cycle: subscriptionCycleMonthly},
			now:       cstNoon(t, "2026-11-25"),
			wantStart: "2026-11-03 00:00", wantEnd: "2026-12-03 00:00", wantDays: 7, wantState: subscriptionStateActive,
		},
		{
			name:      "date-only end expires at 00:00 of that day",
			sub:       authFileSubscription{Start: day("2026-11-03"), Cycle: subscriptionCycleMonthly},
			now:       cstNoon(t, "2026-12-03"),
			wantStart: "2026-11-03 00:00", wantEnd: "2026-12-03 00:00", wantDays: -1, wantSecs: -12 * 3600, checkSecs: true, wantState: subscriptionStateExpired,
		},
		{
			name:      "single period expired does not roll",
			sub:       authFileSubscription{Start: day("2026-09-02"), Cycle: subscriptionCycleMonthly},
			now:       cstNoon(t, "2026-11-02"),
			wantStart: "2026-09-02 00:00", wantEnd: "2026-10-02 00:00", wantDays: -32, wantState: subscriptionStateExpired,
		},
		{
			name:      "month-end anchor single period",
			sub:       authFileSubscription{Start: day("2026-01-31"), Cycle: subscriptionCycleMonthly},
			now:       cstNoon(t, "2026-02-01"),
			wantStart: "2026-01-31 00:00", wantEnd: "2026-02-28 00:00", wantDays: 26, wantState: subscriptionStateActive,
		},
		{
			name:      "auto renew rolls forward k periods without drift",
			sub:       authFileSubscription{Start: day("2026-01-31"), Cycle: subscriptionCycleMonthly, AutoRenew: true},
			now:       cstNoon(t, "2026-04-15"),
			wantStart: "2026-03-31 00:00", wantEnd: "2026-04-30 00:00", wantDays: 14, wantState: subscriptionStateActive,
		},
		{
			name:      "auto renew on renewal day starts new period",
			sub:       authFileSubscription{Start: day("2026-09-02"), Cycle: subscriptionCycleMonthly, AutoRenew: true},
			now:       cstNoon(t, "2026-11-02"),
			wantStart: "2026-11-02 00:00", wantEnd: "2026-12-02 00:00", wantDays: 29, wantState: subscriptionStateActive,
		},
		{
			name:      "auto renew leap year february",
			sub:       authFileSubscription{Start: day("2027-12-31"), Cycle: subscriptionCycleMonthly, AutoRenew: true},
			now:       cstNoon(t, "2028-02-10"),
			wantStart: "2028-01-31 00:00", wantEnd: "2028-02-29 00:00", wantDays: 18, wantState: subscriptionStateActive,
		},
		{
			name:      "auto renew future start stays first period",
			sub:       authFileSubscription{Start: day("2026-12-10"), Cycle: subscriptionCycleMonthly, AutoRenew: true},
			now:       cstNoon(t, "2026-11-02"),
			wantStart: "2026-12-10 00:00", wantEnd: "2027-01-10 00:00", wantDays: 68, wantState: subscriptionStateActive,
		},
		{
			name:      "annual single period",
			sub:       authFileSubscription{Start: day("2026-03-15"), Cycle: subscriptionCycleAnnual},
			now:       cstNoon(t, "2026-11-02"),
			wantStart: "2026-03-15 00:00", wantEnd: "2027-03-15 00:00", wantDays: 132, wantState: subscriptionStateActive,
		},
		{
			name:      "annual leap day auto renew",
			sub:       authFileSubscription{Start: day("2024-02-29"), Cycle: subscriptionCycleAnnual, AutoRenew: true},
			now:       cstNoon(t, "2026-11-02"),
			wantStart: "2026-02-28 00:00", wantEnd: "2027-02-28 00:00", wantDays: 117, wantState: subscriptionStateActive,
		},
		{
			name:      "manual end overrides cycle and auto renew",
			sub:       authFileSubscription{Start: day("2026-09-02"), Cycle: subscriptionCycleMonthly, AutoRenew: true, End: at("2026-11-05 00:00")},
			now:       cstNoon(t, "2026-11-02"),
			wantStart: "2026-11-02 00:00", wantEnd: "2026-11-05 00:00", wantDays: 2, wantState: subscriptionStateWarning,
		},
		{
			name:      "manual end in the past is expired",
			sub:       authFileSubscription{Start: day("2026-09-02"), Cycle: subscriptionCycleMonthly, End: at("2026-10-02 00:00")},
			now:       cstNoon(t, "2026-10-04"),
			wantStart: "2026-09-02 00:00", wantEnd: "2026-10-02 00:00", wantDays: -3, wantState: subscriptionStateExpired,
		},
		{
			// 2026-12-02 15:59 UTC is 2026-12-02 23:59 CST: one minute before expiry.
			name:      "CST boundary one minute before expiry",
			sub:       authFileSubscription{Start: day("2026-11-03"), Cycle: subscriptionCycleMonthly},
			now:       time.Date(2026, 12, 2, 15, 59, 0, 0, time.UTC),
			wantStart: "2026-11-03 00:00", wantEnd: "2026-12-03 00:00", wantDays: 0, wantSecs: 60, checkSecs: true, wantState: subscriptionStateWarning,
		},
		{
			// 2026-12-02 16:00 UTC is exactly 2026-12-03 00:00 CST: now >= expiry.
			name:      "expired exactly at expiry instant",
			sub:       authFileSubscription{Start: day("2026-11-03"), Cycle: subscriptionCycleMonthly},
			now:       time.Date(2026, 12, 2, 16, 0, 0, 0, time.UTC),
			wantStart: "2026-11-03 00:00", wantEnd: "2026-12-03 00:00", wantDays: 0, wantSecs: 0, checkSecs: true, wantState: subscriptionStateExpired,
		},
		{
			name:      "minute start keeps time of day",
			sub:       authFileSubscription{Start: cstAt(t, "2026-10-05 00:35"), StartHasTime: true, Cycle: subscriptionCycleMonthly},
			now:       cstAt(t, "2026-11-03 00:35"),
			wantStart: "2026-10-05 00:35", wantEnd: "2026-11-05 00:35", wantDays: 2, wantSecs: 2 * 86400, checkSecs: true, wantState: subscriptionStateWarning,
		},
		{
			name:      "under one day left reports hours via seconds",
			sub:       authFileSubscription{Start: cstAt(t, "2026-10-05 08:00"), StartHasTime: true, Cycle: subscriptionCycleMonthly},
			now:       cstAt(t, "2026-11-05 03:00"),
			wantStart: "2026-10-05 08:00", wantEnd: "2026-11-05 08:00", wantDays: 0, wantSecs: 5 * 3600, checkSecs: true, wantState: subscriptionStateWarning,
		},
		{
			name:      "minute start expired one minute after expiry",
			sub:       authFileSubscription{Start: cstAt(t, "2026-10-05 08:00"), StartHasTime: true, Cycle: subscriptionCycleMonthly},
			now:       cstAt(t, "2026-11-05 08:01"),
			wantStart: "2026-10-05 08:00", wantEnd: "2026-11-05 08:00", wantDays: -1, wantSecs: -60, checkSecs: true, wantState: subscriptionStateExpired,
		},
		{
			name:      "clamped month end keeps time before renewal",
			sub:       authFileSubscription{Start: cstAt(t, "2026-01-31 23:45"), StartHasTime: true, Cycle: subscriptionCycleMonthly, AutoRenew: true},
			now:       cstAt(t, "2026-02-28 23:44"),
			wantStart: "2026-01-31 23:45", wantEnd: "2026-02-28 23:45", wantDays: 0, wantSecs: 60, checkSecs: true, wantState: subscriptionStateWarning,
		},
		{
			name:      "auto renew rolls at the exact minute",
			sub:       authFileSubscription{Start: cstAt(t, "2026-01-31 23:45"), StartHasTime: true, Cycle: subscriptionCycleMonthly, AutoRenew: true},
			now:       cstAt(t, "2026-02-28 23:45"),
			wantStart: "2026-02-28 23:45", wantEnd: "2026-03-31 23:45", wantDays: 31, wantState: subscriptionStateActive,
		},
		{
			name:      "manual end with minutes",
			sub:       authFileSubscription{Start: day("2026-11-01"), Cycle: subscriptionCycleMonthly, End: at("2026-11-20 18:30"), EndHasTime: true},
			now:       cstAt(t, "2026-11-20 18:00"),
			wantStart: "2026-11-01 00:00", wantEnd: "2026-11-20 18:30", wantDays: 0, wantSecs: 1800, checkSecs: true, wantState: subscriptionStateWarning,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeSubscriptionPeriod(tc.sub, tc.now)
			if s := got.Start.In(subscriptionLocation).Format(subscriptionMinuteLayout); s != tc.wantStart {
				t.Errorf("period start = %s, want %s", s, tc.wantStart)
			}
			if e := got.End.In(subscriptionLocation).Format(subscriptionMinuteLayout); e != tc.wantEnd {
				t.Errorf("period end = %s, want %s", e, tc.wantEnd)
			}
			if got.DaysLeft != tc.wantDays {
				t.Errorf("days left = %d, want %d", got.DaysLeft, tc.wantDays)
			}
			if tc.checkSecs && got.SecondsLeft != tc.wantSecs {
				t.Errorf("seconds left = %d, want %d", got.SecondsLeft, tc.wantSecs)
			}
			if got.State != tc.wantState {
				t.Errorf("state = %s, want %s", got.State, tc.wantState)
			}
		})
	}
}

func TestParseAuthFileSubscriptionValidation(t *testing.T) {
	cases := []struct {
		name    string
		value   any
		wantErr string
	}{
		{"not object", "2026-11-03", "must be an object"},
		{"missing start", map[string]any{"cycle": "monthly"}, "subscription.start is required"},
		{"bad start format", map[string]any{"start": "2026/11/03"}, "subscription.start must be a valid date"},
		{"impossible date", map[string]any{"start": "2026-02-30"}, "subscription.start must be a valid date"},
		{"start not string", map[string]any{"start": json.Number("20261103")}, "subscription.start must be a string"},
		{"bad hour", map[string]any{"start": "2026-11-03 25:00"}, "subscription.start must be a valid date"},
		{"seconds not accepted", map[string]any{"start": "2026-11-03 08:00:00"}, "subscription.start must be a valid date"},
		{"end minute before start minute", map[string]any{"start": "2026-11-03 10:00", "end": "2026-11-03 09:59"}, "must not be before"},
		{"bad cycle", map[string]any{"start": "2026-11-03", "cycle": "weekly"}, "subscription.cycle must be"},
		{"auto_renew not bool", map[string]any{"start": "2026-11-03", "auto_renew": "true"}, "subscription.auto_renew must be a boolean"},
		{"bad end", map[string]any{"start": "2026-11-03", "end": "tomorrow"}, "subscription.end must be a valid date"},
		{"end before start", map[string]any{"start": "2026-11-03", "end": "2026-11-01"}, "must not be before"},
		{"unknown field", map[string]any{"start": "2026-11-03", "plan": "max"}, "subscription.plan is not a supported field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseAuthFileSubscription(tc.value)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}

	sub, err := parseAuthFileSubscription(map[string]any{"start": " 2026-11-03 ", "cycle": "Annual", "end": ""})
	if err != nil {
		t.Fatalf("valid subscription rejected: %v", err)
	}
	if sub.Cycle != subscriptionCycleAnnual || sub.AutoRenew || sub.End != nil {
		t.Fatalf("unexpected parse result: %+v", sub)
	}
	defaults, err := parseAuthFileSubscription(map[string]any{"start": "2026-11-03", "end": nil})
	if err != nil || defaults.Cycle != subscriptionCycleMonthly || defaults.AutoRenew {
		t.Fatalf("defaults = %+v, err = %v", defaults, err)
	}
}

func TestAuthFileSubscriptionPayload(t *testing.T) {
	if _, ok := authFileSubscriptionPayload(map[string]any{"type": "claude"}, time.Now()); ok {
		t.Fatal("payload present without subscription")
	}
	meta := map[string]any{"subscription": map[string]any{"start": "2026-11-03", "cycle": "monthly", "auto_renew": false}}
	payload, ok := authFileSubscriptionPayload(meta, cstNoon(t, "2026-11-10"))
	if !ok {
		t.Fatal("payload missing")
	}
	want := map[string]any{
		"start": "2026-11-03", "cycle": "monthly", "auto_renew": false, "end": nil,
		"period_start": "2026-11-03 00:00", "period_end": "2026-12-03 00:00", "days_left": 22, "state": "active",
		"seconds_left": int64(22*86400 + 12*3600), "expires_at": "2026-12-03T00:00:00+08:00",
	}
	for key, value := range want {
		if payload[key] != value {
			t.Errorf("payload[%s] = %#v, want %#v", key, payload[key], value)
		}
	}

	broken, ok := authFileSubscriptionPayload(map[string]any{"subscription": "garbage"}, time.Now())
	if !ok || broken["state"] != subscriptionStateUnset || broken["error"] == nil {
		t.Fatalf("broken payload = %#v", broken)
	}
}

func TestParseAuthFileSubscriptionMinutePrecisionNormalises(t *testing.T) {
	cases := []struct {
		start, end         string
		wantStart, wantEnd string
	}{
		{"2026-11-03", "", "2026-11-03", ""},
		{"2026-11-03 08:05", "2026-12-03", "2026-11-03 08:05", "2026-12-03"},
		{" 2026-11-03T08:05 ", "2026-12-03T18:30", "2026-11-03 08:05", "2026-12-03 18:30"},
		{"2026-11-03 00:00", "2026-11-03 00:00", "2026-11-03 00:00", "2026-11-03 00:00"},
	}
	for _, tc := range cases {
		input := map[string]any{"start": tc.start}
		if tc.end != "" {
			input["end"] = tc.end
		}
		sub, err := parseAuthFileSubscription(input)
		if err != nil {
			t.Fatalf("%q/%q rejected: %v", tc.start, tc.end, err)
		}
		stored := sub.metadataValue()
		if stored["start"] != tc.wantStart {
			t.Errorf("stored start = %v, want %s", stored["start"], tc.wantStart)
		}
		if tc.wantEnd == "" {
			if _, has := stored["end"]; has {
				t.Errorf("stored end = %v, want omitted", stored["end"])
			}
		} else if stored["end"] != tc.wantEnd {
			t.Errorf("stored end = %v, want %s", stored["end"], tc.wantEnd)
		}
		// Round trip keeps the same canonical value.
		again, err := parseAuthFileSubscription(stored)
		if err != nil || again.metadataValue()["start"] != stored["start"] {
			t.Errorf("round trip of %#v failed: %v", stored, err)
		}
	}
}

func TestAuthFileSubscriptionPayloadMinutePrecision(t *testing.T) {
	meta := map[string]any{"subscription": map[string]any{"start": "2026-10-05 00:35", "cycle": "monthly", "auto_renew": false}}
	payload, ok := authFileSubscriptionPayload(meta, cstAt(t, "2026-11-04 12:35"))
	if !ok {
		t.Fatal("payload missing")
	}
	want := map[string]any{
		"start": "2026-10-05 00:35", "period_start": "2026-10-05 00:35", "period_end": "2026-11-05 00:35",
		"expires_at": "2026-11-05T00:35:00+08:00", "days_left": 0, "seconds_left": int64(12 * 3600), "state": "warning",
	}
	for key, value := range want {
		if payload[key] != value {
			t.Errorf("payload[%s] = %#v, want %#v", key, payload[key], value)
		}
	}
}
