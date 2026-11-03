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
//	"subscription": {"start": "2026-11-03", "cycle": "monthly", "auto_renew": false, "end": "2026-12-03"}
//
// Dates are calendar dates in Asia/Shanghai. Billing periods follow Stripe's
// calendar-month anchoring: each period end is start + k*cycle computed from the
// original anchor and clamped to the month end (Jan 31 -> Feb 28/29 -> Mar 31).

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

	subscriptionWarningDays = 7
	subscriptionDateLayout  = "2006-01-02"

	// maxSubscriptionRollPeriods bounds the auto-renew roll-forward loop.
	maxSubscriptionRollPeriods = 12 * 500
)

// subscriptionLocation is the zone used to decide "today" (Asia/Shanghai, no DST).
var subscriptionLocation = time.FixedZone("CST", 8*3600)

// authFileSubscription is the validated form of the subscription metadata.
// Dates are stored as UTC midnight values so day arithmetic is exact.
type authFileSubscription struct {
	Start     time.Time
	Cycle     string
	AutoRenew bool
	End       *time.Time
}

// authFileSubscriptionPeriod is the computed current period.
type authFileSubscriptionPeriod struct {
	Start    time.Time
	End      time.Time
	DaysLeft int
	State    string
}

func subscriptionCycleMonths(cycle string) int {
	if cycle == subscriptionCycleAnnual {
		return 12
	}
	return 1
}

func subscriptionDate(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// addMonthsClamped adds months to anchor, clamping the day to the target month end.
// Callers must always pass the original anchor (anchor + k*months) so the day never drifts.
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
	return subscriptionDate(targetYear, targetMonth, day)
}

// subscriptionToday returns today's calendar date in Asia/Shanghai as UTC midnight.
func subscriptionToday(now time.Time) time.Time {
	year, month, day := now.In(subscriptionLocation).Date()
	return subscriptionDate(year, month, day)
}

func subscriptionDaysBetween(from, to time.Time) int {
	return int(to.Sub(from).Hours() / 24)
}

func parseSubscriptionDate(field string, value any) (time.Time, error) {
	text, ok := value.(string)
	if !ok {
		return time.Time{}, fmt.Errorf("subscription.%s must be a date string in YYYY-MM-DD format", field)
	}
	text = strings.TrimSpace(text)
	parsed, err := time.Parse(subscriptionDateLayout, text)
	if err != nil {
		return time.Time{}, fmt.Errorf("subscription.%s must be a valid date in YYYY-MM-DD format, got %q", field, text)
	}
	return subscriptionDate(parsed.Year(), parsed.Month(), parsed.Day()), nil
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
	start, err := parseSubscriptionDate(subscriptionFieldStart, rawStart)
	if err != nil {
		return authFileSubscription{}, err
	}
	sub.Start = start

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
		end, errEnd := parseSubscriptionDate(subscriptionFieldEnd, rawEnd)
		if errEnd != nil {
			return authFileSubscription{}, errEnd
		}
		if end.Before(sub.Start) {
			return authFileSubscription{}, fmt.Errorf("subscription.end must not be before subscription.start")
		}
		sub.End = &end
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
		subscriptionFieldStart:     s.Start.Format(subscriptionDateLayout),
		subscriptionFieldCycle:     s.Cycle,
		subscriptionFieldAutoRenew: s.AutoRenew,
	}
	if s.End != nil {
		out[subscriptionFieldEnd] = s.End.Format(subscriptionDateLayout)
	}
	return out
}

// computeSubscriptionPeriod resolves the current billing period relative to now (CST calendar day).
func computeSubscriptionPeriod(sub authFileSubscription, now time.Time) authFileSubscriptionPeriod {
	today := subscriptionToday(now)
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
		for k := 1; k <= maxSubscriptionRollPeriods && !today.Before(period.End); k++ {
			period.Start = period.End
			period.End = addMonthsClamped(sub.Start, (k+1)*months)
		}
	}

	period.DaysLeft = subscriptionDaysBetween(today, period.End)
	switch {
	case period.DaysLeft < 0:
		period.State = subscriptionStateExpired
	case period.DaysLeft <= subscriptionWarningDays:
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
	payload["period_start"] = period.Start.Format(subscriptionDateLayout)
	payload["period_end"] = period.End.Format(subscriptionDateLayout)
	payload["days_left"] = period.DaysLeft
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
