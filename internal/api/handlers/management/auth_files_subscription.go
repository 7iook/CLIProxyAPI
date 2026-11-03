package management

import (
	"fmt"
	"strings"
	"time"
)

// Manually maintained subscription metadata for auth files.
//
// Stored in the auth file JSON as:
//
//	"subscription": {"start": "2026-11-03", "cycle": "monthly", "auto_renew": false, "end": "2026-12-03 18:30"}
//
// start and end are Asia/Shanghai wall-clock values with minute precision. They are
// accepted as "YYYY-MM-DD" (00:00), "YYYY-MM-DD HH:mm" or "YYYY-MM-DDTHH:mm" and stored
// normalised: a date-only value stays "YYYY-MM-DD", a timed value becomes "YYYY-MM-DD HH:mm".
// Billing periods follow Stripe's calendar-month anchoring: each period end is
// start + k*cycle computed from the original anchor, clamped to the month end
// (Jan 31 -> Feb 28/29 -> Mar 31), keeping the time of day.

const (
	authFileSubscriptionKey = "subscription"

	subscriptionFieldStart     = "start"
	subscriptionFieldCycle     = "cycle"
	subscriptionFieldAutoRenew = "auto_renew"
	subscriptionFieldEnd       = "end"

	subscriptionCycleMonthly = "monthly"
	subscriptionCycleAnnual  = "annual"

	subscriptionStateActive  = "active"
	subscriptionStateWarning = "warning"
	subscriptionStateExpired = "expired"
	subscriptionStateUnset   = "unset"

	subscriptionWarningDays    = 7
	subscriptionSecondsPerDay  = 24 * 60 * 60
	subscriptionDateLayout     = "2006-01-02"
	subscriptionMinuteLayout   = "2006-01-02 15:04"
	subscriptionMinuteLayoutT  = "2006-01-02T15:04"
	subscriptionValueFormatMsg = "YYYY-MM-DD or YYYY-MM-DD HH:mm"

	// maxSubscriptionRollPeriods bounds the auto-renew roll-forward loop.
	maxSubscriptionRollPeriods = 12 * 500
)

// subscriptionLocation is the zone all subscription values are interpreted in (Asia/Shanghai, no DST).
var subscriptionLocation = time.FixedZone("CST", 8*3600)

// authFileSubscription is the validated form of the subscription metadata.
// Start and End are instants in subscriptionLocation; the HasTime flags remember
// whether a time of day was entered so the stored format round-trips.
type authFileSubscription struct {
	Start        time.Time
	StartHasTime bool
	Cycle        string
	AutoRenew    bool
	End          *time.Time
	EndHasTime   bool
}

// authFileSubscriptionPeriod is the computed current period.
type authFileSubscriptionPeriod struct {
	Start       time.Time
	End         time.Time
	SecondsLeft int64
	DaysLeft    int
	State       string
}

func subscriptionCycleMonths(cycle string) int {
	if cycle == subscriptionCycleAnnual {
		return 12
	}
	return 1
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// addMonthsClamped adds months to anchor, clamping the day to the target month end and
// keeping the time of day and location. Callers must always pass the original anchor
// (anchor + k*months) so the day never drifts.
func addMonthsClamped(anchor time.Time, months int) time.Time {
	year, month, day := anchor.Date()
	total := int(month) - 1 + months
	yearOffset := total / 12
	monthIndex := total % 12
	if monthIndex < 0 {
		monthIndex += 12
		yearOffset--
	}
	targetYear := year + yearOffset
	targetMonth := time.Month(monthIndex + 1)
	if last := daysInMonth(targetYear, targetMonth); day > last {
		day = last
	}
	return time.Date(targetYear, targetMonth, day, anchor.Hour(), anchor.Minute(), 0, 0, anchor.Location())
}

// floorDiv divides rounding toward negative infinity.
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// parseSubscriptionTime parses a start/end value in Asia/Shanghai and reports whether
// a time of day was given.
func parseSubscriptionTime(field string, value any) (time.Time, bool, error) {
	text, ok := value.(string)
	if !ok {
		return time.Time{}, false, fmt.Errorf("subscription.%s must be a string in %s format", field, subscriptionValueFormatMsg)
	}
	text = strings.TrimSpace(text)
	if parsed, err := time.ParseInLocation(subscriptionDateLayout, text, subscriptionLocation); err == nil {
		return parsed, false, nil
	}
	for _, layout := range []string{subscriptionMinuteLayout, subscriptionMinuteLayoutT} {
		if parsed, err := time.ParseInLocation(layout, text, subscriptionLocation); err == nil {
			return parsed, true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("subscription.%s must be a valid date in %s format, got %q", field, subscriptionValueFormatMsg, text)
}

func formatSubscriptionValue(t time.Time, hasTime bool) string {
	if hasTime {
		return t.In(subscriptionLocation).Format(subscriptionMinuteLayout)
	}
	return t.In(subscriptionLocation).Format(subscriptionDateLayout)
}

// parseAuthFileSubscription validates a subscription object (as decoded from JSON).
func parseAuthFileSubscription(value any) (authFileSubscription, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return authFileSubscription{}, fmt.Errorf("subscription must be an object or null")
	}
	for key := range object {
		switch key {
		case subscriptionFieldStart, subscriptionFieldCycle, subscriptionFieldAutoRenew, subscriptionFieldEnd:
		default:
			return authFileSubscription{}, fmt.Errorf("subscription.%s is not a supported field (allowed: start, cycle, auto_renew, end)", key)
		}
	}

	var sub authFileSubscription
	rawStart, okStart := object[subscriptionFieldStart]
	if !okStart || rawStart == nil {
		return authFileSubscription{}, fmt.Errorf("subscription.start is required (send subscription: null to remove the subscription)")
	}
	start, startHasTime, err := parseSubscriptionTime(subscriptionFieldStart, rawStart)
	if err != nil {
		return authFileSubscription{}, err
	}
	sub.Start, sub.StartHasTime = start, startHasTime

	sub.Cycle = subscriptionCycleMonthly
	if rawCycle, okCycle := object[subscriptionFieldCycle]; okCycle && rawCycle != nil {
		cycle, okString := rawCycle.(string)
		cycle = strings.ToLower(strings.TrimSpace(cycle))
		if !okString || (cycle != subscriptionCycleMonthly && cycle != subscriptionCycleAnnual) {
			return authFileSubscription{}, fmt.Errorf("subscription.cycle must be \"monthly\" or \"annual\"")
		}
		sub.Cycle = cycle
	}

	if rawAutoRenew, okAutoRenew := object[subscriptionFieldAutoRenew]; okAutoRenew && rawAutoRenew != nil {
		autoRenew, okBool := rawAutoRenew.(bool)
		if !okBool {
			return authFileSubscription{}, fmt.Errorf("subscription.auto_renew must be a boolean")
		}
		sub.AutoRenew = autoRenew
	}

	// An empty string clears the manual end date, same as null.
	if rawEnd, okEnd := object[subscriptionFieldEnd]; okEnd && rawEnd != nil && !isBlankString(rawEnd) {
		end, endHasTime, errEnd := parseSubscriptionTime(subscriptionFieldEnd, rawEnd)
		if errEnd != nil {
			return authFileSubscription{}, errEnd
		}
		if end.Before(sub.Start) {
			return authFileSubscription{}, fmt.Errorf("subscription.end must not be before subscription.start")
		}
		sub.End, sub.EndHasTime = &end, endHasTime
	}
	return sub, nil
}

func isBlankString(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) == ""
}

// metadataValue returns the canonical JSON-friendly form stored in the auth file.
func (s authFileSubscription) metadataValue() map[string]any {
	out := map[string]any{
		subscriptionFieldStart:     formatSubscriptionValue(s.Start, s.StartHasTime),
		subscriptionFieldCycle:     s.Cycle,
		subscriptionFieldAutoRenew: s.AutoRenew,
	}
	if s.End != nil {
		out[subscriptionFieldEnd] = formatSubscriptionValue(*s.End, s.EndHasTime)
	}
	return out
}

// computeSubscriptionPeriod resolves the current billing period relative to now.
// The subscription is expired once now reaches the period end instant.
func computeSubscriptionPeriod(sub authFileSubscription, now time.Time) authFileSubscriptionPeriod {
	now = now.In(subscriptionLocation)
	months := subscriptionCycleMonths(sub.Cycle)

	period := authFileSubscriptionPeriod{Start: sub.Start}
	switch {
	case sub.End != nil:
		period.End = *sub.End
		// Period start is the latest anchor occurrence strictly before the manual end.
		for k := 1; k <= maxSubscriptionRollPeriods; k++ {
			next := addMonthsClamped(sub.Start, k*months)
			if !next.Before(period.End) {
				break
			}
			period.Start = next
		}
	case !sub.AutoRenew:
		period.End = addMonthsClamped(sub.Start, months)
	default:
		period.End = addMonthsClamped(sub.Start, months)
		for k := 1; k <= maxSubscriptionRollPeriods && !now.Before(period.End); k++ {
			period.Start = period.End
			period.End = addMonthsClamped(sub.Start, (k+1)*months)
		}
	}

	period.SecondsLeft = int64(period.End.Sub(now) / time.Second)
	period.DaysLeft = int(floorDiv(period.SecondsLeft, subscriptionSecondsPerDay))
	switch {
	case period.SecondsLeft <= 0:
		period.State = subscriptionStateExpired
	case period.SecondsLeft <= subscriptionWarningDays*subscriptionSecondsPerDay:
		period.State = subscriptionStateWarning
	default:
		period.State = subscriptionStateActive
	}
	return period
}

// authFileSubscriptionPayload builds the list-entry echo for the subscription metadata.
// ok is false when no subscription is stored.
func authFileSubscriptionPayload(metadata map[string]any, now time.Time) (map[string]any, bool) {
	if metadata == nil {
		return nil, false
	}
	raw, exists := metadata[authFileSubscriptionKey]
	if !exists || raw == nil {
		return nil, false
	}
	sub, err := parseAuthFileSubscription(raw)
	if err != nil {
		// Hand-edited or corrupted value: surface it without computed fields.
		return map[string]any{
			"raw":   raw,
			"state": subscriptionStateUnset,
			"error": err.Error(),
		}, true
	}
	period := computeSubscriptionPeriod(sub, now)
	payload := sub.metadataValue()
	if _, hasEnd := payload[subscriptionFieldEnd]; !hasEnd {
		payload[subscriptionFieldEnd] = nil
	}
	payload["period_start"] = period.Start.In(subscriptionLocation).Format(subscriptionMinuteLayout)
	payload["period_end"] = period.End.In(subscriptionLocation).Format(subscriptionMinuteLayout)
	payload["expires_at"] = period.End.In(subscriptionLocation).Format(time.RFC3339)
	payload["days_left"] = period.DaysLeft
	payload["seconds_left"] = period.SecondsLeft
	payload["state"] = period.State
	return payload, true
}

// hasConflictingSubscriptionPatch reports whether a patch sets both the whole
// subscription object and one of its nested fields, which would be order dependent.
func hasConflictingSubscriptionPatch(fieldPaths []string) bool {
	whole, nested := false, false
	for _, path := range fieldPaths {
		path = strings.TrimSpace(path)
		if path == authFileSubscriptionKey {
			whole = true
		} else if rootAuthFileField(path) == authFileSubscriptionKey {
			nested = true
		}
	}
	return whole && nested
}

// applyAuthFileSubscriptionPatch applies a PATCH for "subscription" or "subscription.<field>".
// null on the root removes the field entirely; null on a nested field clears that field.
func applyAuthFileSubscriptionPatch(metadata map[string]any, fieldPath string, value any) error {
	if metadata == nil {
		return fmt.Errorf("metadata is nil")
	}
	fieldPath = strings.TrimSpace(fieldPath)
	if fieldPath == authFileSubscriptionKey {
		if value == nil {
			delete(metadata, authFileSubscriptionKey)
			return nil
		}
		sub, err := parseAuthFileSubscription(value)
		if err != nil {
			return err
		}
		metadata[authFileSubscriptionKey] = sub.metadataValue()
		return nil
	}

	field := strings.TrimSpace(strings.TrimPrefix(fieldPath, authFileSubscriptionKey+"."))
	switch field {
	case subscriptionFieldStart, subscriptionFieldCycle, subscriptionFieldAutoRenew, subscriptionFieldEnd:
	default:
		return fmt.Errorf("subscription.%s is not a supported field (allowed: start, cycle, auto_renew, end)", field)
	}

	next := make(map[string]any)
	if existing, ok := metadata[authFileSubscriptionKey].(map[string]any); ok {
		for key, val := range existing {
			next[key] = val
		}
	}
	if value == nil {
		delete(next, field)
	} else {
		next[field] = value
	}
	sub, err := parseAuthFileSubscription(next)
	if err != nil {
		return err
	}
	metadata[authFileSubscriptionKey] = sub.metadataValue()
	return nil
}
