# deploy/ec2 — single-box EC2 bootstrap (test posture)

`bootstrap.sh` (minimal API core) and `bootstrap-full.sh` (whole product)
stand up SeDoc on one Ubuntu EC2 instance from prebuilt images, using the
dev compose stack. They run the **dev / test posture**: they write
`SEDOC_ENVIRONMENT=dev` to `.env` explicitly, along with
`SEDOC_ALLOW_BYPASS_RLS=1`, and they serve HTTP unless you put Caddy in
front. Use them for demos, pilots, UAT and API testing, never for production.

Production uses the Helm chart (`deploy/helm/sedoc`) or the Ansible bundle
(`deploy/ansible`). Both set `SEDOC_ENVIRONMENT=prod`.

On every run the scripts append any of these keys missing from `.env`, with
fresh random values. Existing values are never rotated:

- `SEDOC_INTERNAL_API_KEY`
- `SEDOC_NATS_PASSWORD`
- `SEDOC_ONLYOFFICE_JWT`

Guides: `docs/deploy/ec2-api-check.md`, `docs/deploy/ec2-full-stack.md`.
