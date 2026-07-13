//go:build integration
// +build integration

// Push-MFA ack flow against real Postgres + Redis (ADR 0122): the
// rubber-stamp bypass is dead, forged/replayed/expired/revoked acks are
// rejected (forgeries without consuming the challenge), and a properly
// signed device ack passes exactly once.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/notifications"
	"github.com/aieera/sedoc/pkg/testutil"
)

// pushAckFixtureDDL — organizations + minimal users + the verbatim
// user_push_devices shape (document migration 000029). No RLS: the
// container superuser drives the flow; posture is the prod-posture
// suite's concern.
const pushAckFixtureDDL = `
CREATE TABLE organizations (
	id         UUID PRIMARY KEY,
	deleted_at TIMESTAMPTZ
);
CREATE TABLE users (
	tenant_id UUID NOT NULL REFERENCES organizations(id),
	id        UUID NOT NULL,
	PRIMARY KEY (tenant_id, id)
);
CREATE TABLE user_push_devices (
	tenant_id      UUID        NOT NULL REFERENCES organizations(id),
	id             UUID        NOT NULL DEFAULT gen_random_uuid(),
	user_id        UUID        NOT NULL,
	platform       TEXT        NOT NULL CHECK (platform IN ('fcm', 'apns')),
	token          TEXT        NOT NULL,
	ack_key_sealed BYTEA       NOT NULL,
	label          TEXT        NOT NULL,
	created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
	last_used_at   TIMESTAMPTZ,
	revoked_at     TIMESTAMPTZ,
	PRIMARY KEY (tenant_id, id),
	UNIQUE (tenant_id, user_id, token),
	FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id) ON DELETE CASCADE
);
`

type pushHarness struct {
	ctx    context.Context
	svc    *Service
	rdb    *redis.Client
	tenant uuid.UUID
	user   uuid.UUID
}

func newPushHarness(t *testing.T) *pushHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	dsn, pgClean, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgClean)
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, pushAckFixtureDDL)
	require.NoError(t, err)

	redisURL, redisClean, err := testutil.NewRedisContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(redisClean)
	ropts, err := redis.ParseURL(redisURL)
	require.NoError(t, err)
	rdb := redis.NewClient(ropts)
	t.Cleanup(func() { _ = rdb.Close() })

	tenant := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, tenant)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO users (tenant_id, id) VALUES ($1, $2)`, tenant, user)
	require.NoError(t, err)

	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 1)
	}
	svc := &Service{pool: pool, rdb: rdb, localKek: kek, now: time.Now}
	return &pushHarness{ctx: ctx, svc: svc, rdb: rdb, tenant: tenant, user: user}
}

// seedChallenge writes a pending challenge exactly as StartPushChallenge
// does and returns its id.
func (h *pushHarness) seedChallenge(t *testing.T, expiresIn time.Duration) string {
	t.Helper()
	id := uuid.New().String()
	now := time.Now()
	ch := &notifications.PushChallenge{
		ChallengeID: id,
		UserID:      h.user.String(),
		TenantID:    h.tenant.String(),
		IssuedAt:    now.Unix(),
		ExpiresAt:   now.Add(expiresIn).Unix(),
	}
	body, _ := json.Marshal(ch)
	require.NoError(t, h.rdb.Set(h.ctx, pushChallengeKey(id), body, 2*time.Minute).Err())
	return id
}

func (h *pushHarness) challengeExists(t *testing.T, id string) bool {
	t.Helper()
	n, err := h.rdb.Exists(h.ctx, pushChallengeKey(id)).Result()
	require.NoError(t, err)
	return n == 1
}

func (h *pushHarness) registerDevice(t *testing.T) *RegisteredPushDevice {
	t.Helper()
	dev, err := h.svc.RegisterPushDevice(h.ctx, h.tenant, h.user, notifications.PushDevice{
		Platform: notifications.PlatformFCM,
		Token:    "expo-token-" + uuid.NewString(),
		Label:    "test phone",
	})
	require.NoError(t, err)
	require.NotEmpty(t, dev.AckKey)
	return dev
}

func TestPushAck_RubberStampBypassIsDead(t *testing.T) {
	h := newPushHarness(t)
	h.registerDevice(t)
	chal := h.seedChallenge(t, 2*time.Minute)

	// THE bypass: any non-empty ack used to be treated as approval.
	for _, ack := range []string{"x", "approved", "true", uuid.NewString()} {
		err := h.svc.validatePushAck(h.ctx, h.tenant, h.user, chal, ack)
		require.ErrorIs(t, err, vdmserr.ErrUnauthorized,
			"unsigned ack %q must be rejected", ack)
	}
	require.True(t, h.challengeExists(t, chal),
		"failed attempts must NOT consume the challenge")
}

func TestPushAck_ForgedSignatureRejected(t *testing.T) {
	h := newPushHarness(t)
	dev := h.registerDevice(t)
	chal := h.seedChallenge(t, 2*time.Minute)

	// Right device id, wrong key.
	forged := dev.DeviceID + "." + signPushAck("not-the-ack-key", chal, dev.DeviceID)
	err := h.svc.validatePushAck(h.ctx, h.tenant, h.user, chal, forged)
	require.ErrorIs(t, err, vdmserr.ErrUnauthorized)

	// Right key, signed for a DIFFERENT challenge (cross-challenge replay).
	otherChal := h.seedChallenge(t, 2*time.Minute)
	cross := dev.DeviceID + "." + signPushAck(dev.AckKey, otherChal, dev.DeviceID)
	err = h.svc.validatePushAck(h.ctx, h.tenant, h.user, chal, cross)
	require.ErrorIs(t, err, vdmserr.ErrUnauthorized, "signature must be bound to the challenge")

	require.True(t, h.challengeExists(t, chal), "forgeries must not consume the challenge")
}

func TestPushAck_HappyPathThenReplayRejected(t *testing.T) {
	h := newPushHarness(t)
	dev := h.registerDevice(t)
	chal := h.seedChallenge(t, 2*time.Minute)

	ack := dev.DeviceID + "." + signPushAck(dev.AckKey, chal, dev.DeviceID)
	require.NoError(t, h.svc.validatePushAck(h.ctx, h.tenant, h.user, chal, ack),
		"a properly signed device ack must pass")
	require.False(t, h.challengeExists(t, chal), "success consumes the challenge (one-time use)")

	// Replaying the SAME valid ack must fail — the challenge is gone.
	err := h.svc.validatePushAck(h.ctx, h.tenant, h.user, chal, ack)
	require.ErrorIs(t, err, vdmserr.ErrUnauthorized, "replayed ack must be rejected")
}

func TestPushAck_ExpiredChallengeRejected(t *testing.T) {
	h := newPushHarness(t)
	dev := h.registerDevice(t)
	chal := h.seedChallenge(t, -1*time.Second) // already expired

	ack := dev.DeviceID + "." + signPushAck(dev.AckKey, chal, dev.DeviceID)
	err := h.svc.validatePushAck(h.ctx, h.tenant, h.user, chal, ack)
	require.ErrorIs(t, err, vdmserr.ErrUnauthorized, "expired challenge must be rejected even with a valid signature")
}

func TestPushAck_RevokedDeviceRejected(t *testing.T) {
	h := newPushHarness(t)
	dev := h.registerDevice(t)
	_, err := h.svc.pool.Exec(h.ctx,
		`UPDATE user_push_devices SET revoked_at = now() WHERE tenant_id = $1 AND id = $2`,
		h.tenant, uuid.MustParse(dev.DeviceID))
	require.NoError(t, err)

	chal := h.seedChallenge(t, 2*time.Minute)
	ack := dev.DeviceID + "." + signPushAck(dev.AckKey, chal, dev.DeviceID)
	err = h.svc.validatePushAck(h.ctx, h.tenant, h.user, chal, ack)
	require.ErrorIs(t, err, vdmserr.ErrUnauthorized, "a revoked device's key must no longer pass")
}

func TestPushAck_OtherUsersDeviceRejected(t *testing.T) {
	h := newPushHarness(t)
	dev := h.registerDevice(t)

	// The challenge belongs to a DIFFERENT user in the same tenant.
	mallory := uuid.Must(uuid.NewV7())
	_, err := h.svc.pool.Exec(h.ctx, `INSERT INTO users (tenant_id, id) VALUES ($1, $2)`, h.tenant, mallory)
	require.NoError(t, err)
	chal := h.seedChallenge(t, 2*time.Minute) // bound to h.user

	ack := dev.DeviceID + "." + signPushAck(dev.AckKey, chal, dev.DeviceID)
	err = h.svc.validatePushAck(h.ctx, h.tenant, mallory, chal, ack)
	require.Error(t, err, "an ack cannot complete another user's challenge")
	require.True(t, errors.Is(err, vdmserr.ErrUnauthorized))
}
