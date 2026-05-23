// Phase 3 — workspace settings backend pinning tests.
//
// What we pin without a Postgres tx:
//   - workspaceIsUserEmpty: the loosened empty-check that allows
//     DeleteWorkspace to remove a brand-new workspace whose only
//     folder is the auto-created "Root".
//
// Service flows that mutate Postgres (TransferWorkspaceOwnership,
// DeleteWorkspace happy-path) need a real tx + outbox and live in the
// integration suite under -tags=integration.
package service

import (
	"testing"

	"github.com/google/uuid"

	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

func TestWorkspaceIsUserEmpty(t *testing.T) {
	cases := []struct {
		name    string
		docs    int
		folders int
		want    bool
	}{
		// Brand-new workspace: only the auto-Root folder, no documents.
		// This must be deletable — that's the entire reason for the
		// rule change. Pre-Phase-3, FolderCount==1 forced a 409.
		{name: "brand-new (auto-Root only)", docs: 0, folders: 1, want: true},

		// Truly empty (no folders at all, somehow): also deletable.
		{name: "fully empty", docs: 0, folders: 0, want: true},

		// Any document blocks delete — never loosen this.
		{name: "one document", docs: 1, folders: 0, want: false},
		{name: "one document + auto-Root", docs: 1, folders: 1, want: false},

		// More than one folder = user has organised content; refuse.
		{name: "two folders, no docs", docs: 0, folders: 2, want: false},
		{name: "many folders, no docs", docs: 0, folders: 7, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &model.Workspace{
				ID:            uuid.New(),
				TenantID:      uuid.New(),
				DocumentCount: int64(tc.docs),
				FolderCount:   int64(tc.folders),
			}
			if got := workspaceIsUserEmpty(w); got != tc.want {
				t.Errorf("workspaceIsUserEmpty(docs=%d folders=%d) = %v, want %v",
					tc.docs, tc.folders, got, tc.want)
			}
		})
	}
}
