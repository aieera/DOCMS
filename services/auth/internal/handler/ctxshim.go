package handler

import (
	"net/http"

	"github.com/vaultdms/vaultdms/pkg/auth"
)

// authUserInfo is a thin alias to pkg/auth.UserInfo so handler files avoid a
// full import graph to the pkg package for one struct.
type authUserInfo = auth.UserInfo

func authUserFromCtx(r *http.Request) (authUserInfo, error) {
	return auth.User(r.Context())
}
