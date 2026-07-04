// ADR 0086 — Decide() pre-delivery gate + digest flush ticker.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aieera/sedoc/services/notification/internal/model"
)

// digestWindow is the per-event-type accumulation window.
// 5 min matches ADR 0086.
const digestWindow = 300

// ChannelConsent is the emitter-supplied per-event channel consent
// (DeliveryPayload.Channels, parsed): channel → forceDigest. A
// consented channel is delivered even without a matrix cell, because
// the recipient explicitly opted into it for this event kind (e.g. a
// saved-search subscription). Consent NEVER overrides snooze, DND, or
// an explicit is_enabled=false matrix cell for the exact event type.
type ChannelConsent map[model.Channel]bool

// ParseChannelConsent maps DeliveryPayload.Channels strings onto the
// consent set. "digest" means email folded through the digest table.
// Unknown strings are ignored. Returns nil when nothing recognized.
func ParseChannelConsent(channels []string) ChannelConsent {
	if len(channels) == 0 {
		return nil
	}
	c := ChannelConsent{}
	for _, raw := range channels {
		switch raw {
		case "in_app":
			c[model.ChannelInApp] = false
		case "email":
			// Don't clobber a digest=true from an earlier "digest".
			if !c[model.ChannelEmail] {
				c[model.ChannelEmail] = false
			}
		case "push":
			c[model.ChannelPush] = false
		case "digest":
			c[model.ChannelEmail] = true
		}
	}
	if len(c) == 0 {
		return nil
	}
	return c
}

// Decide runs the unified preferences pipeline for one (user, event)
// pair. Returns the channels to deliver on RIGHT NOW. A digest-on
// channel is folded into the digest accumulator and excluded from
// the immediate-delivery list.
//
// Pipeline:
//  1. Snooze active for (user, event_type) → return nil (skip user).
//  2. DND active in user's tz → strip everything except in_app.
//  3. For each enabled cell whose channel is in `requested`:
//     - cell.digest_enabled → UpsertDigestEvent, exclude from list.
//     - else → include channel.
//
// consent (nil-able) overlays emitter-supplied per-event channel
// consent AFTER the matrix: it can enable a channel that has no
// matrix cell, but not one the user explicitly disabled.
func (s *Service) Decide(ctx context.Context, tenantID, userID, eventType string, requested []model.Channel, consent ChannelConsent, eventPayload map[string]any) ([]model.Channel, error) {
	snoozed, err := s.repo.IsSnoozed(ctx, tenantID, userID, eventType)
	if err != nil {
		return nil, fmt.Errorf("snooze check: %w", err)
	}
	if snoozed {
		return nil, nil
	}

	dnd, err := s.repo.GetDND(ctx, tenantID, userID)
	if err != nil {
		return nil, fmt.Errorf("dnd lookup: %w", err)
	}
	inDND := dnd != nil && inDNDWindow(dnd, time.Now())

	cells, err := s.repo.ListMatrix(ctx, tenantID, userID)
	if err != nil {
		return nil, fmt.Errorf("matrix lookup: %w", err)
	}
	enabled := indexEnabledCells(cells, eventType)
	// Default policy: if no rows for this event_type at all, in-app AND
	// push are on (immediate), everything else off. Push rides the
	// default because registering a device is itself the opt-in signal —
	// without this, a fresh user's registered phone never buzzed unless
	// they also hand-built a matrix cell (review finding, ADR 0117).
	// Users silence push via the matrix (is_enabled=false cell) or the
	// flat push_enabled switch; no registered devices = no-op anyway.
	if len(enabled) == 0 {
		enabled = map[model.Channel]bool{
			model.ChannelInApp: false,
			model.ChannelPush:  false,
		}
	}
	applyConsent(enabled, explicitlyDisabled(cells, eventType), consent)

	out := make([]model.Channel, 0, len(requested))
	for _, ch := range requested {
		digest, ok := enabled[ch]
		if !ok {
			continue
		}
		if inDND && ch != model.ChannelInApp {
			continue
		}
		if digest {
			d := model.Digest{
				TenantID: tenantID, UserID: userID,
				EventType: eventType, Channel: string(ch),
			}
			if err := s.repo.UpsertDigestEvent(ctx, d, eventPayload, digestWindow); err != nil {
				s.log.Error().Err(err).Str("user_id", userID).Str("event_type", eventType).Str("channel", string(ch)).Msg("digest accumulate failed")
				// Fall through to immediate delivery so the user
				// doesn't silently lose this notification.
				out = append(out, ch)
			}
			continue
		}
		out = append(out, ch)
	}
	return out, nil
}

// applyConsent overlays emitter consent onto the matrix verdict:
// each consented channel is enabled unless the user explicitly
// disabled it for this exact event type. A consent digest=true wins
// over an immediate-mode matrix cell — the recipient asked for a
// digest on this event kind specifically.
func applyConsent(enabled map[model.Channel]bool, disabled map[model.Channel]struct{}, consent ChannelConsent) {
	for ch, forceDigest := range consent {
		if _, off := disabled[ch]; off {
			continue
		}
		if forceDigest {
			enabled[ch] = true
			continue
		}
		if _, ok := enabled[ch]; !ok {
			enabled[ch] = false
		}
	}
}

// explicitlyDisabled collects channels with an is_enabled=false matrix
// row for the exact event type — the one signal consent must respect.
func explicitlyDisabled(cells []model.PrefCell, eventType string) map[model.Channel]struct{} {
	out := map[model.Channel]struct{}{}
	for _, c := range cells {
		if !c.IsEnabled && c.EventType == eventType {
			out[model.Channel(c.Channel)] = struct{}{}
		}
	}
	return out
}

// indexEnabledCells reduces a user's matrix rows to a map of
// channel → digest_enabled, restricted to rows where is_enabled and
// event_type matches. Wildcard ('*') rows fall through if no
// event-type-specific row exists.
func indexEnabledCells(cells []model.PrefCell, eventType string) map[model.Channel]bool {
	exact := map[model.Channel]bool{}
	wildcard := map[model.Channel]bool{}
	for _, c := range cells {
		if !c.IsEnabled {
			continue
		}
		ch := model.Channel(c.Channel)
		switch c.EventType {
		case eventType:
			exact[ch] = c.DigestEnabled
		case "*":
			wildcard[ch] = c.DigestEnabled
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return wildcard
}

// inDNDWindow reports whether `now` falls inside the user's DND
// window in their declared timezone. Handles wrap-past-midnight
// (e.g. 22:00 → 07:00) by treating start>end as a window that wraps.
func inDNDWindow(d *model.DND, now time.Time) bool {
	loc, err := time.LoadLocation(d.Timezone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	curMin := local.Hour()*60 + local.Minute()
	startMin, ok1 := parseHHMM(d.DNDStart)
	endMin, ok2 := parseHHMM(d.DNDEnd)
	if !ok1 || !ok2 {
		return false
	}
	if startMin == endMin {
		return false // no window
	}
	if startMin < endMin {
		return curMin >= startMin && curMin < endMin
	}
	// wraps midnight
	return curMin >= startMin || curMin < endMin
}

func parseHHMM(s string) (int, bool) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	var h, m int
	if _, err := fmt.Sscanf(parts[0], "%d", &h); err != nil {
		return 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &m); err != nil {
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// ----- Digest flush ticker ---------------------------------------

// StartDigestFlusher runs the 1-minute sweep loop until ctx cancels.
// Each tick claims any ripe digests and emits one summary
// notification per claimed row through the existing Deliver path.
func (s *Service) StartDigestFlusher(ctx context.Context) {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.flushDigestsOnce(ctx)
		}
	}
}

func (s *Service) flushDigestsOnce(ctx context.Context) {
	digests, err := s.repo.ClaimReadyDigests(ctx, 100)
	if err != nil {
		s.log.Error().Err(err).Msg("claim digests")
		return
	}
	for _, d := range digests {
		title, body := summarizeDigest(d)
		// One synthetic notification per digest. We bypass Decide
		// here — the user already opted into digest mode and the
		// preferences/snooze checks happened when each event was
		// folded in.
		payload := model.DeliveryPayload{
			TenantID: d.TenantID,
			UserIDs:  []string{d.UserID},
			Type:     "digest." + d.EventType,
			Title:    title,
			Body:     body,
			// The user opted into a digest ON THIS CHANNEL when the
			// events were folded in; without this consent hint the
			// summary would be dropped to in-app-only (no matrix cell
			// exists for the synthetic "digest.*" event type).
			Channels: []string{d.Channel},
		}
		if err := s.Deliver(ctx, payload); err != nil {
			s.log.Error().Err(err).Str("digest_id", d.ID).Msg("flush digest deliver")
		}
	}
}

// summarizeDigest produces a "3 new comment mentions" style title +
// a short body listing the first three event subjects. First-cut
// formatting per ADR 0086 "out of scope" — channel-specific HTML
// templating lands later.
func summarizeDigest(d model.Digest) (title, body string) {
	title = fmt.Sprintf("%d new %s", d.Count, humanizeEventType(d.EventType))
	if d.Count > 1 {
		title += "s"
	}
	var arr []map[string]any
	if err := json.Unmarshal(d.Events, &arr); err != nil || len(arr) == 0 {
		return title, ""
	}
	var lines []string
	limit := 3
	if len(arr) < limit {
		limit = len(arr)
	}
	for i := 0; i < limit; i++ {
		if t, ok := arr[i]["title"].(string); ok && t != "" {
			lines = append(lines, "• "+t)
		}
	}
	if extra := d.Count - limit; extra > 0 {
		lines = append(lines, fmt.Sprintf("…and %d more", extra))
	}
	body = strings.Join(lines, "\n")
	return title, body
}

func humanizeEventType(t string) string {
	// "comment.mention" → "comment mention". Good enough for a
	// digest title; the UI can prettify further.
	return strings.ReplaceAll(t, ".", " ")
}
