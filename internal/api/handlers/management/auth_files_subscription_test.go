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

func TestComputeSubscriptionPeriod(t *testing.T) {
	date := func(v string) *time.Time { d := mustSubDate(t, v); return &d }
	cases := []struct {
		name      string
		sub       authFileSubscription
		now       time.Time
		wantStart string
		wantEnd   string
		wantDays  int
		wantState string
	}{
		{
			name:      "single monthly period active",
			sub:       authFileSubscription{Start: mustSubDate(t, "2026-11-03"), Cycle: subscriptionCycleMonthly},
			now:       cstNoon(t, "2026-11-03"),
			wantStart: "2026-11-03", wantEnd: "2026-12-03", wantDays: 30, wantState: subscriptionStateActive,
		},
		{
			name:      "warning at 7 days",
			sub:       authFileSubscription{Start: mustSubDate(t, "2026-11-03"), Cycle: subscriptionCycleMonthly},
			now:       cstNoon(t, "2026-11-26"),
			wantStart: "2026-11-03", wantEnd: "2026-12-03", wantDays: 7, wantState: subscriptionStateWarning,
		},
		{
			name:      "active at 8 days",
			sub:       authFileSubscription{Start: mustSubDate(t, "2026-11-03"), Cycle: subscriptionCycleMonthly},
			now:       cstNoon(t, "2026-11-25"),
			wantStart: "2026-11-03", wantEnd: "2026-12-03", wantDays: 8, wantState: subscriptionStateActive,
		},
		{
			name:      "exactly on end date is warning with 0 days",
			sub:       authFileSubscription{Start: mustSubDate(t, "2026-11-03"), Cycle: subscriptionCycleMonthly},
			now:       cstNoon(t, "2026-12-03"),
			wantStart: "2026-11-03", wantEnd: "2026-12-03", wantDays: 0, wantState: subscriptionStateWarning,
		},
		{
			name:      "single period expired does not roll",
			sub:       authFileSubscription{Start: mustSubDate(t, "2026-09-02"), Cycle: subscriptionCycleMonthly},
			now:       cstNoon(t, "2026-11-02"),
			wantStart: "2026-09-02", wantEnd: "2026-10-02", wantDays: -31, wantState: subscriptionStateExpired,
		},
		{
			name:      "month-end anchor single period",
			sub:       authFileSubscription{Start: mustSubDate(t, "2026-01-31"), Cycle: subscriptionCycleMonthly},
			now:       cstNoon(t, "2026-02-01"),
			wantStart: "2026-01-31", wantEnd: "2026-02-28", wantDays: 27, wantState: subscriptionStateActive,
		},
		{
			name:      "auto renew rolls forward k periods without drift",
			sub:       authFileSubscription{Start: mustSubDate(t, "2026-01-31"), Cycle: subscriptionCycleMonthly, AutoRenew: true},
			now:       cstNoon(t, "2026-04-15"),
			wantStart: "2026-03-31", wantEnd: "2026-04-30", wantDays: 15, wantState: subscriptionStateActive,
		},
		{
			name:      "auto renew on renewal day starts new period",
			sub:       authFileSubscription{Start: mustSubDate(t, "2026-09-02"), Cycle: subscriptionCycleMonthly, AutoRenew: true},
			now:       cstNoon(t, "2026-11-02"),
			wantStart: "2026-11-02", wantEnd: "2026-12-02", wantDays: 30, wantState: subscriptionStateActive,
		},
		{
			name:      "auto renew leap year february",
			sub:       authFileSubscription{Start: mustSubDate(t, "2027-12-31"), Cycle: subscriptionCycleMonthly, AutoRenew: true},
			now:       cstNoon(t, "2028-02-10"),
			wantStart: "2028-01-31", wantEnd: "2028-02-29", wantDays: 19, wantState: subscriptionStateActive,
		},
		{
			name:      "auto renew future start stays first period",
			sub:       authFileSubscription{Start: mustSubDate(t, "2026-12-10"), Cycle: subscriptionCycleMonthly, AutoRenew: true},
			now:       cstNoon(t, "2026-11-02"),
			wantStart: "2026-12-10", wantEnd: "2027-01-10", wantDays: 69, wantState: subscriptionStateActive,
		},
		{
			name:      "annual single period",
			sub:       authFileSubscription{Start: mustSubDate(t, "2026-03-15"), Cycle: subscriptionCycleAnnual},
			now:       cstNoon(t, "2026-11-02"),
			wantStart: "2026-03-15", wantEnd: "2027-03-15", wantDays: 133, wantState: subscriptionStateActive,
		},
		{
			name:      "annual leap day auto renew",
			sub:       authFileSubscription{Start: mustSubDate(t, "2024-02-29"), Cycle: subscriptionCycleAnnual, AutoRenew: true},
			now:       cstNoon(t, "2026-11-02"),
			wantStart: "2026-02-28", wantEnd: "2027-02-28", wantDays: 118, wantState: subscriptionStateActive,
		},
		{
			name:      "manual end overrides cycle and auto renew",
			sub:       authFileSubscription{Start: mustSubDate(t, "2026-09-02"), Cycle: subscriptionCycleMonthly, AutoRenew: true, End: date("2026-11-05")},
			now:       cstNoon(t, "2026-11-02"),
			wantStart: "2026-11-02", wantEnd: "2026-11-05", wantDays: 3, wantState: subscriptionStateWarning,
		},
		{
			name:      "manual end in the past is expired",
			sub:       authFileSubscription{Start: mustSubDate(t, "2026-09-02"), Cycle: subscriptionCycleMonthly, End: date("2026-10-02")},
			now:       cstNoon(t, "2026-10-04"),
			wantStart: "2026-09-02", wantEnd: "2026-10-02", wantDays: -2, wantState: subscriptionStateExpired,
		},
		{
			// 2026-12-02 16:30 UTC is already 2026-12-03 00:30 in CST: end day reached.
			name:      "CST day boundary at UTC 16:30",
			sub:       authFileSubscription{Start: mustSubDate(t, "2026-11-03"), Cycle: subscriptionCycleMonthly},
			now:       time.Date(2026, 12, 2, 16, 30, 0, 0, time.UTC),
			wantStart: "2026-11-03", wantEnd: "2026-12-03", wantDays: 0, wantState: subscriptionStateWarning,
		},
		{
			// 2026-12-03 15:59 UTC is still 2026-12-03 in CST; 16:00 UTC is 12-04.
			name:      "CST day boundary flips to expired at UTC 16:00",
			sub:       authFileSubscription{Start: mustSubDate(t, "2026-11-03"), Cycle: subscriptionCycleMonthly},
			now:       time.Date(2026, 12, 3, 16, 0, 0, 0, time.UTC),
			wantStart: "2026-11-03", wantEnd: "2026-12-03", wantDays: -1, wantState: subscriptionStateExpired,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeSubscriptionPeriod(tc.sub, tc.now)
			if s := got.Start.Format(subscriptionDateLayout); s != tc.wantStart {
				t.Errorf("period start = %s, want %s", s, tc.wantStart)
			}
			if e := got.End.Format(subscriptionDateLayout); e != tc.wantEnd {
				t.Errorf("period end = %s, want %s", e, tc.wantEnd)
			}
			if got.DaysLeft != tc.wantDays {
				t.Errorf("days left = %d, want %d", got.DaysLeft, tc.wantDays)
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
		{"start not string", map[string]any{"start": json.Number("20261103")}, "subscription.start must be a date string"},
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
		"period_start": "2026-11-03", "period_end": "2026-12-03", "days_left": 23, "state": "active",
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
