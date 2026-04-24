# Wave 15 — 90-second demo script

**Target length:** 90 s.
**Output path:** `docs/demo/wave-15.mp4` (or a Loom link recorded in
this file once the capture is done).
**Browser:** Chrome, 1440×900, dark mode off, zoom 100%. Record at
30 fps.

The four clips below each run end-to-end against the local dev
stack (`make docker-up && make run-web`) using the seed tenant
`acme` / `admin@acme.local`. The Playwright route mocks in
`web/e2e/11..14-*.spec.ts` cover the same flows — use them to verify
selectors before recording.

## Clip 1 — Force-change-password (≈20 s)

1. Pre-condition: `admin` (or seeded `compliance-officer@acme.local`)
   has `must_change_password = true`. Set via:
   ```sh
   dms-admin users force-password-reset --tenant acme --email demo@acme.local
   ```
2. Open an incognito window, sign in with the pre-reset user.
3. Show the redirect to `/change-password`. Pause on the
   requirements checklist — each row should tick as the password is
   typed. Show the show/hide toggle.
4. Submit. Expect the dashboard.

## Clip 2 — Geofence deny (≈25 s)

1. From the admin account, `/admin/geofences`.
2. Create: scope `tenant`, mode `deny`, country `CN`, apply `*`.
3. Open the dry-run tester, paste a Chinese Alibaba IP
   (e.g. `47.88.0.1`). Expect `DENY (country_deny)`.
4. Delete the policy or disable after recording so the rest of the
   demo isn't blocked.

## Clip 3 — Acknowledgement create + ack (≈30 s)

1. From the compliance-officer account, `/admin/acknowledgements` →
   **New campaign**.
2. Fill: document UUID (any existing doc), title "Annual policy",
   due date 14 days out, one recipient UUID (the demo user's id),
   **Create & activate**.
3. Sign out, sign in as the recipient.
4. Dashboard shows the amber `AckBanner` with "1 pending
   acknowledgement". Click through to `/acknowledgements`.
5. Click **I have read and understood**. Banner + row disappear; the
   empty state "All caught up" renders.
6. Back in the admin account, the per-campaign report shows
   acknowledged=1, ack-rate=100%.

## Clip 4 — Saved signature (≈15 s)

1. `/settings/signatures`. Show the "No saved signatures" empty
   state.
2. Draw a signature in the canvas. Name it "My sig". Check **Make
   default**. Save.
3. Row appears with the rendered image + **Default** badge.

## Recording checklist

- [ ] Disable any toast-auto-dismiss that hides confirmation toasts
      before the frame captures them (set `duration: 4000` in
      `web/src/main.tsx` if needed, restore after).
- [ ] Pre-seed the four pre-conditions so the camera doesn't catch
      the setup path:
      - Force-reset user exists and is pre-flagged.
      - Demo document id in clipboard for the ack-create step.
      - Policy table is empty before Clip 2 and after.
- [ ] Hide browser dev-tools + any unread-notification counts.
- [ ] Record at 30 fps → export as H.264 MP4 ≤ 20 MB; commit to
      `docs/demo/wave-15.mp4`. If file is oversize, embed a Loom
      link in this doc instead.
