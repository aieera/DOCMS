// Mobile push fan-out (ADR 0117). Delivery goes through the Expo push
// service (pkg/notifications.ExpoClient) addressed by the per-device
// tokens users register via POST /api/v1/notifications/devices.
package service

import (
	"context"

	"github.com/aieera/sedoc/pkg/notifications"
	"github.com/aieera/sedoc/services/notification/internal/model"
)

// PushTransport abstracts the Expo client so tests can fake it.
type PushTransport interface {
	Send(ctx context.Context, msgs []notifications.ExpoMessage) ([]notifications.ExpoTicket, error)
}

// SetPushTransport wires the mobile push sender. Nil (the default)
// disables the push channel — in-app/email delivery is unaffected.
func (s *Service) SetPushTransport(t PushTransport) { s.push = t }

// RegisterPushDevice, ListPushDevices, DeletePushDevice — thin facade
// over the repository for the devices handler.
func (s *Service) RegisterPushDevice(ctx context.Context, d *model.PushDevice) error {
	return s.repo.UpsertPushDevice(ctx, d)
}

func (s *Service) ListPushDevices(ctx context.Context, tenantID, userID string) ([]model.PushDevice, error) {
	return s.repo.ListPushDevices(ctx, tenantID, userID)
}

func (s *Service) DeletePushDevice(ctx context.Context, tenantID, userID, deviceID string) (bool, error) {
	return s.repo.DeletePushDevice(ctx, tenantID, userID, deviceID)
}

// sendPush fans one notification out to every active device of the
// recipient. Failures are logged, never fatal — the in-app row has
// already landed, push is opportunistic. Tokens Expo reports as
// DeviceNotRegistered are revoked so the fleet self-heals.
func (s *Service) sendPush(ctx context.Context, n *model.Notification) {
	if s.push == nil {
		return
	}
	devices, err := s.repo.ListPushDevices(ctx, n.TenantID, n.UserID)
	if err != nil {
		s.log.Warn().Err(err).Str("user_id", n.UserID).Msg("push: list devices")
		return
	}
	if len(devices) == 0 {
		return
	}
	msgs := make([]notifications.ExpoMessage, 0, len(devices))
	tokens := make([]string, 0, len(devices))
	for _, d := range devices {
		// Only the Expo transport is wired; fcm/apns rows wait for
		// their native adapters.
		if d.Platform != "expo" {
			continue
		}
		msgs = append(msgs, notifications.ExpoMessage{
			To:    d.Token,
			Title: n.Title,
			Body:  n.Body,
			Sound: "default",
			Data: map[string]string{
				"notification_id": n.ID,
				"type":            n.Type,
				"resource_type":   n.ResourceType,
				"resource_id":     n.ResourceID,
			},
		})
		tokens = append(tokens, d.Token)
	}
	if len(msgs) == 0 {
		return
	}
	tickets, err := s.push.Send(ctx, msgs)
	if err != nil {
		s.log.Warn().Err(err).Str("user_id", n.UserID).Msg("push: send")
		return
	}
	var dead []string
	for i, t := range tickets {
		if t.DeviceNotRegistered() {
			dead = append(dead, tokens[i])
		}
	}
	if len(dead) > 0 {
		if err := s.repo.RevokePushTokens(ctx, n.TenantID, dead); err != nil {
			s.log.Warn().Err(err).Int("count", len(dead)).Msg("push: revoke dead tokens")
		} else {
			s.log.Info().Int("count", len(dead)).Msg("push: revoked DeviceNotRegistered tokens")
		}
	}
}
