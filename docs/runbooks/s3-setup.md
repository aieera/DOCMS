# S3 setup (object store)

VaultDMS uses one S3-API-compatible object store per environment. The
code path is identical across environments — only the endpoint +
credentials differ.

## Environment matrix

| Env | Backend | Endpoint | Credentials | Addressing |
|---|---|---|---|---|
| local | MinIO (docker-compose) | `http://minio:9000` | `minioadmin / minioadmin` from compose | path |
| staging | AWS S3 | `https://s3.us-east-1.amazonaws.com` | IRSA via ServiceAccount annotation | virtual |
| prod | AWS S3 | `https://s3.us-east-1.amazonaws.com` | IRSA via ServiceAccount annotation | virtual |

## Local (MinIO)

Zero setup beyond `make up`. The compose file includes `minio` + a
one-shot `minio-init` container that provisions the 5 buckets:

```
dms-us-east-1-hot
dms-us-east-1-warm
dms-us-east-1-archive
dms-us-east-1-quarantine
dms-us-east-1-previews
```

`make gen-env` writes the MinIO values into `.env`. To switch
between MinIO and AWS templates locally:

```bash
bash scripts/switch-s3-backend.sh local    # from .env.local.example
bash scripts/switch-s3-backend.sh aws      # from .env.aws.example
```

## Staging + prod (AWS S3 + IRSA)

1. **Provision the infra** via [deploy/terraform/s3/](../../deploy/terraform/s3/):

   ```bash
   cd deploy/terraform/s3
   terraform apply \
     -var environment=staging \
     -var region=us-east-1 \
     -var web_origin=https://staging.app.vaultdms.example \
     -var cluster_oidc_provider_arn=arn:aws:iam::ACCT:oidc-provider/...
   ```

   Outputs the 5 bucket names + the KMS CMK ARN + the IRSA role ARN.

2. **Annotate the storage ServiceAccount** with the IRSA role:

   ```yaml
   # values-aws.yaml
   storage:
     serviceAccount:
       annotations:
         eks.amazonaws.com/role-arn: arn:aws:iam::ACCT:role/vaultdms-staging-storage
   ```

3. **Set Helm values**:

   ```yaml
   global:
     s3:
       endpoint: "https://s3.us-east-1.amazonaws.com"
       region: us-east-1
       useSSL: true
       # accessKeySecret NOT used with IRSA — leave blank or omit.
   ```

4. **Verify** after `helm install`:

   ```bash
   kubectl -n vaultdms exec deploy/vaultdms-storage -- \
     env | grep VAULTDMS_S3_
   # VAULTDMS_S3_ENDPOINT=https://s3.us-east-1.amazonaws.com
   # VAULTDMS_S3_USE_SSL=true

   kubectl -n vaultdms exec deploy/vaultdms-storage -- \
     wget -q -O- http://localhost:8081/healthz
   # {"status":"healthy"}
   ```

## Per-tenant KEK wrapping

Irrespective of backend, every blob is envelope-encrypted with a
per-tenant DEK wrapped by a per-tenant KEK alias. On AWS, the KEK
alias resolves to the shared KMS CMK provisioned by the Terraform
module. See [runbook 06 — key management](./06-key-management.md).

## Rename history

Wave 15 / 2026-04-20: renamed env vars `VAULTDMS_MINIO_*` →
`VAULTDMS_S3_*` across Go config, Helm, and CI. MinIO stays as the
local-dev backend; production uses AWS S3. A CI guard
(`scripts/check-no-minio-leaks.sh`) forbids MinIO-specific naming
from re-entering service/pkg/helm paths.

## Troubleshooting

**Storage service 500s on upload with `InvalidAccessKeyId`** — env
is still pointing at MinIO's admin credentials against AWS S3.
Confirm `VAULTDMS_S3_ACCESS_KEY` is EMPTY in the pod env (IRSA
provides creds via the metadata service) and that the SA has the
`eks.amazonaws.com/role-arn` annotation.

**"SignatureDoesNotMatch" on presigned URLs** — addressing-style
mismatch. AWS wants virtual (`bucket.s3…`), MinIO wants path
(`host/bucket/...`). Set `S3_ADDRESSING_STYLE` to `virtual` for AWS.
