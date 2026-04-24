# VaultDMS S3 + KMS + IRSA (AWS)

Provisions the 5-bucket topology, KMS CMK, and IAM role for IRSA for
one environment (staging or prod). Apply once per environment.

## Layout

| Bucket | Purpose |
|---|---|
| `dms-${env}-objects`  | encrypted document blobs (primary) |
| `dms-${env}-previews` | thumbnails + paginated page previews |
| `dms-${env}-exports`  | DSR export ZIPs + audit CSV exports |
| `dms-${env}-dsr`      | DSR verification-token scratch space |
| `dms-${env}-backups`  | cross-region PITR + point-in-time object copies |

Every bucket: SSE-KMS, versioning on, all 4 public-access-blocks set, 7-day abort-incomplete-multipart lifecycle. CORS only on `objects` (browser direct-PUT); all others are backend-only.

## Usage

```bash
cd deploy/terraform/s3
terraform init
terraform apply \
  -var environment=staging \
  -var region=us-east-1 \
  -var web_origin=https://staging.app.vaultdms.example \
  -var cluster_oidc_provider_arn=arn:aws:iam::123456789012:oidc-provider/oidc.eks.us-east-1.amazonaws.com/id/XXXX
```

Outputs:
- `bucket_names` — map of purpose → bucket name.
- `kms_key_arn` — for `VAULTDMS_KMS_KEY_ARN` in Helm values.
- `irsa_role_arn` — annotate on the storage ServiceAccount: `eks.amazonaws.com/role-arn: <arn>`.

## IRSA binding

The role's trust policy is scoped to the `vaultdms-storage` SA in the `vaultdms` namespace. Change the `namespace` variable if installing elsewhere.

## Verification

After apply:

```bash
aws s3api get-bucket-versioning --bucket dms-staging-objects       # Status=Enabled
aws s3api get-public-access-block --bucket dms-staging-objects    # all 4 true
aws s3api get-bucket-encryption  --bucket dms-staging-objects      # SSE-KMS
aws s3api get-bucket-lifecycle-configuration --bucket dms-staging-objects | jq '.Rules[0]'
aws iam get-role --role-name vaultdms-staging-storage
```

## Caveats

- The assume-role condition uses an `StringEquals` on `<oidc-provider>:sub` equal to `system:serviceaccount:${namespace}:vaultdms-storage`. If IRSA is used by other VaultDMS services (document, signature read the S3 client too), add their SAs to the condition.
- KMS CMK has `deletion_window_in_days = 30` — matches the tenant-disposal 30-day grace documented in `docs/runbooks/13-control-plane.md`.
