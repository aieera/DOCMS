// Profile mutations for the authenticated user — self-service edits
// of the columns on `users` that don't touch role/status (those go
// through admin.go's owner-gated flows). Today this only covers
// `locale`; future tier-0 additions like timezone or display-name
// editing land here.
package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/model"
)

// SupportedLocales is the set we accept. Mirror this list when
// adding a new namespace bundle to web/public/locales/; the DB
// CHECK constraint in 000051_user_locale.up.sql is the second gate
// and MUST be updated in the same change.
var SupportedLocales = map[string]struct{}{
	"en": {},
	"ar": {},
}

// UpdateLocale persists a new UI-language preference for the caller.
// Returns ErrInvalidArgument if `locale` isn't in SupportedLocales —
// the handler maps that to 400 before even opening a tx, so we
// don't burn a connection on an obviously-bad request. Refusing
// unknown values here also means a buggy frontend that sends "fr"
// fails fast instead of writing an unreachable preference.
func (s *Service) UpdateLocale(ctx context.Context, tenantID, userID uuid.UUID, locale string) (*model.User, error) {
	if _, ok := SupportedLocales[locale]; !ok {
		return nil, vdmserr.Validation("locale", "unsupported locale")
	}
	var u *model.User
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := s.users.SetLocale(ctx, tx, tenantID, userID, locale); err != nil {
			return err
		}
		got, err := s.users.GetByID(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		u = got
		return nil
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}
