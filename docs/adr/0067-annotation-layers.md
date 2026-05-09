# ADR 0067 — Annotation Layers

Date: 2026-05-09
Status: Accepted

## Context

VaultDMS already ships PDF-only annotations:

- Schema: [services/document/migrations/000001_initial_schema.up.sql:465](../../services/document/migrations/000001_initial_schema.up.sql#L465)
  with CHECK `annotation_type IN ('highlight','note','stamp','drawing')`.
- Service + handler: [annotations.go](../../services/document/internal/service/annotations.go),
  [annotation_handler.go](../../services/document/internal/handler/annotation_handler.go).
- Frontend overlay: PDF.js custom layer rendering the four
  highlight/note/stamp/drawing primitives.

§10.5 widens this to three category-level types:

- `pdf_markup` — every PDF-paper interaction (the existing four
  primitives reshape under one umbrella; the per-primitive shape
  lives in `payload_json` rather than a separate enum value).
- `image_shape` — Fabric.js JSON for raster + vector image previews.
- `video_timestamp` — time-coded comment pins on the preview-service
  video player.

Plus a permission split: `annotation.create` and `annotation.delete`
become separate capabilities from `edit`. Today a user with read but
no edit can't annotate — that's the behaviour customers ask to
relax (legal reviewers want to mark up a contract they're not
allowed to overwrite).

## Decision

### Schema

Migration 000032 widens the CHECK on `annotations.annotation_type`
to add the three category values alongside the existing four. The
old values stay valid so in-flight rows keep working; the service
layer will canonicalize new writes to the category form.

```sql
ALTER TABLE annotations DROP CONSTRAINT annotations_annotation_type_check;
ALTER TABLE annotations ADD CONSTRAINT annotations_annotation_type_check
  CHECK (annotation_type IN (
    -- legacy primitives (kept for back-compat with existing rows)
    'highlight','note','stamp','drawing',
    -- ADR 0067 categories
    'pdf_markup','image_shape','video_timestamp'
  ));
```

The migration also adds two policy capabilities:

```sql
INSERT INTO permissions_catalog (capability, description) VALUES
  ('annotation.create', 'Create annotations on a document'),
  ('annotation.delete', 'Delete annotations on a document')
ON CONFLICT DO NOTHING;
```

### Payload shapes

`annotation_data` (JSONB) holds the type-specific payload. The
backend stays loose — it stores the JSON verbatim — but the
frontend canonicalizes:

| `type` | `annotation_data` shape | Producer |
|---|---|---|
| `pdf_markup` | `{kind: "highlight"\|"underline"\|"strikethrough"\|"note"\|"drawing", page, rects?: [...], color?, body?, path?}` | PDF.js custom layer |
| `image_shape` | A Fabric.js `toJSON()` envelope: `{version, objects: [...]}` | Fabric.js canvas |
| `video_timestamp` | `{at_seconds: number, body: string}` | Preview-service video player |

Standoff: every annotation type stays out-of-band — never burned
into the source PDF / image / video. Rendering is always
front-end overlay on top of the original blob.

### Permission split

Today's `edit` capability still allows everything. The new fine-
grained pair lets admins grant comment/markup-only access:

- `annotation.create` — required for `POST /annotations`.
- `annotation.delete` — required for `DELETE /annotations/{id}`
  when the caller is NOT the original author. Authors can always
  delete their own annotations regardless.
- `annotation.update` is the same as create (edit your own; admin
  override allowed but rare in practice).

Resolution order in the service's `requireAnnotationPermission`:

1. If the user has `edit` on the document → allow (back-compat).
2. Else if action is `create` AND the user has `annotation.create`
   on the document → allow.
3. Else if action is `delete` AND (the caller is the author OR has
   `annotation.delete`) → allow.
4. Else 403.

### Real-time

Annotations already emit `dms.annotation.{created,updated,deleted}.v1`
([annotations.go:107](../../services/document/internal/service/annotations.go#L107))
via the outbox. Collaboration WS service consumes those subjects and
forwards to subscribed clients in the document room. ADR 0067
adds nothing here — the existing fanout works for the new
type values.

### Hide/show toggle

Frontend-only. The toggle lives in the viewer header and flips a
`display: none` on the annotation overlay layer. No persistence —
each user's toggle is per-session. (A future tenant-level "default
on" preference is a small follow-up.)

## Consequences

- One migration. No data movement; old rows keep their
  primitive-form `annotation_type` values, the service treats them
  as `pdf_markup` at the rendering layer.
- The legacy `IsValidAnnotationType` widens; nothing else changes
  in the create/list/delete shape.
- The fine-grained permissions need to land in OPA's policy.rego
  catalog. Today's policy treats unknown capabilities as 403, so a
  forgotten Rego entry fails closed — which is the right default.
- Video annotations require the preview service to emit playable
  video previews. That's already in place for MP4/WebM via
  ffmpeg-thumbnailer; non-playable formats fall back to the
  download-only path with annotations disabled.

## Out of scope

- Inline PDF burn-in for export. Today an exported PDF doesn't
  carry the annotations; future work could rasterize annotations
  into a watermark layer using the existing redaction flatten
  path.
- Cross-version annotation porting ("re-attach my markup to the
  new revision"). Annotations are version-pinned by design.
- Annotation search. Not indexed by the search service today;
  future ADR.
