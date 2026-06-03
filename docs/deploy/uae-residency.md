# UAE residency deployment runbook (ADR 0110)

Audience: SRE deploying SeDoc for a UAE-residency tenant. Pre-reqs:
AWS account in me-central-1, terraform ≥ 1.5, helm ≥ 3.12, kubectl
+ aws-cli configured, the
`deploy/regions/uae-central.yaml` overlay from this repo.

If this is your first time, **read ADR 0110 first**. It explains
why the deployment topology looks the way it does.

---

## 1. AWS account setup

A UAE-residency cluster MUST live in its own AWS account (or its
own OU under AWS Organizations with SCPs blocking cross-region
service usage). Sharing the prod account with US workloads makes
the residency claim hard to defend in a CBUAE audit.

```sh
# Account-level guardrails — apply once at account creation.
aws s3control put-public-access-block \
  --account-id $UAE_ACCOUNT_ID \
  --public-access-block-configuration \
    BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true

# Enable IAM Access Analyzer scoped to me-central-1 so cross-region
# accidents surface in Security Hub.
aws accessanalyzer create-analyzer \
  --analyzer-name vaultdms-uae-residency \
  --type ACCOUNT \
  --region me-central-1
```

Apply an SCP at the OU level denying every region except
`me-central-1` for S3 / RDS / KMS / OpenSearch:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Deny",
    "Action": ["s3:*","rds:*","kms:*","es:*","secretsmanager:*"],
    "Resource": "*",
    "Condition": {
      "StringNotEquals": {"aws:RequestedRegion": "me-central-1"}
    }
  }]
}
```

The SCP is the residency claim's hardest line of defence — every
other layer (NetworkPolicy, middleware, Helm overlay) is
defence-in-depth on top of it.

---

## 2. KMS keys

Create one Customer Master Key in me-central-1 with NO
multi-region replicas:

```sh
aws kms create-key \
  --region me-central-1 \
  --description "SeDoc UAE per-tenant KEK wrapper" \
  --key-usage ENCRYPT_DECRYPT \
  --key-spec SYMMETRIC_DEFAULT \
  --multi-region false \
  --tags TagKey=vaultdms_environment_class,TagValue=uae-residency
aws kms create-alias \
  --region me-central-1 \
  --alias-name alias/vaultdms-uae-central-kek \
  --target-key-id <key-id>
```

Verify the key has NO cross-region grants:

```sh
aws kms list-grants --key-id <key-id> --region me-central-1
# Output should be empty. If grants exist, audit them — a grant
# to an outside-region role is a residency violation.
```

A separate CMK for content-decryption-key (CDK) wrapping is
recommended but not mandatory; the storage service can be
configured with a single KEK alias for the entire UAE deployment
since each tenant's DEK is wrapped independently inside that KEK.

---

## 3. S3 buckets

Create the four buckets named in the overlay
(`dms-me-central-1-{hot,warm,previews,quarantine}`) with these
properties:

```sh
for BUCKET in hot warm previews quarantine; do
  aws s3api create-bucket \
    --region me-central-1 \
    --create-bucket-configuration LocationConstraint=me-central-1 \
    --bucket dms-me-central-1-$BUCKET
  aws s3api put-public-access-block \
    --bucket dms-me-central-1-$BUCKET \
    --public-access-block-configuration \
      BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
  aws s3api put-bucket-encryption \
    --bucket dms-me-central-1-$BUCKET \
    --server-side-encryption-configuration '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"aws:kms","KMSMasterKeyID":"alias/vaultdms-uae-central-kek"}}]}'
  aws s3api put-bucket-ownership-controls \
    --bucket dms-me-central-1-$BUCKET \
    --ownership-controls 'Rules=[{ObjectOwnership=BucketOwnerEnforced}]'
done
```

Object Lock (compliance mode) is required for the `hot` bucket if
retention policies will be used — enable it at create-time with
`--object-lock-enabled-for-bucket true`. Compliance mode is
irrevocable, so don't enable it without confirming the retention
policy first.

Verify replication is **NOT** configured:

```sh
aws s3api get-bucket-replication --bucket dms-me-central-1-hot
# Expect: ReplicationConfigurationNotFoundError. If you see a
# configuration here, delete it — cross-region replication is
# the failure mode this whole runbook exists to prevent.
```

---

## 4. RDS Aurora cluster

Provision an Aurora Postgres cluster in me-central-1 with three
AZs. **Do not** create read replicas in any other region.

```sh
aws rds create-db-cluster \
  --region me-central-1 \
  --db-cluster-identifier vaultdms-uae-prod \
  --engine aurora-postgresql --engine-version 16.2 \
  --master-username vaultdms \
  --manage-master-user-password \
  --master-user-secret-kms-key-id alias/vaultdms-uae-central-kek \
  --kms-key-id alias/vaultdms-uae-central-kek \
  --storage-encrypted \
  --backup-retention-period 35 \
  --vpc-security-group-ids sg-XXX \
  --db-subnet-group-name vaultdms-uae \
  --availability-zones me-central-1a me-central-1b me-central-1c
```

Confirm no cross-region backup target is configured:

```sh
aws backup list-backup-plans --region me-central-1 \
  | jq '.BackupPlansList[].BackupPlan.Rules[] | select(.CopyActions != null)'
# Expect: empty. Any output here means a backup plan is replicating
# snapshots across regions — fix before proceeding.
```

---

## 5. OpenSearch domain

```sh
aws opensearch create-domain \
  --region me-central-1 \
  --domain-name vaultdms-uae \
  --engine-version OpenSearch_2.12 \
  --cluster-config InstanceType=r6g.large.search,InstanceCount=3,ZoneAwarenessEnabled=true,ZoneAwarenessConfig={AvailabilityZoneCount=3} \
  --vpc-options SubnetIds=subnet-AAA,subnet-BBB,subnet-CCC,SecurityGroupIds=sg-XXX \
  --encryption-at-rest-options Enabled=true,KmsKeyId=alias/vaultdms-uae-central-kek \
  --node-to-node-encryption-options Enabled=true \
  --domain-endpoint-options EnforceHTTPS=true,TLSSecurityPolicy=Policy-Min-TLS-1-2-2019-07
```

---

## 6. EKS + Helm apply

Assuming the VPC + subnets + EKS cluster already exist (created
by your standard infra terraform):

```sh
aws eks update-kubeconfig --region me-central-1 --name vaultdms-uae

helm upgrade --install vaultdms ./deploy/helm/vaultdms \
  --namespace vaultdms --create-namespace \
  --values ./deploy/helm/vaultdms/values.yaml \
  --values ./deploy/regions/uae-central.yaml \
  --set global.s3.bucketsHotName=dms-me-central-1-hot \
  --wait --timeout 25m
```

Confirm every service started with the right region:

```sh
for SVC in document auth policy storage search audit workflow signature notification connector billing graphql-gateway mcp-server; do
  kubectl exec deploy/$SVC -- wget -qO- http://localhost:8081/healthz \
    | jq -r --arg svc "$SVC" '"\($svc) → region=\(.region) service=\(.service)"'
done
```

Every line should read `region=uae-central`. Any line that reads
`region=unknown` means the operator forgot the env / left out the
overlay; abort the rollout, fix the values file, redeploy.

---

## 7. NetworkPolicy verification

The overlay ships an egress NetworkPolicy with `denyDefault: true`.
Confirm it's installed AND that the CNI honours FQDN selectors:

```sh
kubectl get networkpolicy -n vaultdms
# Expect at least: vaultdms-egress-allowlist

# Curl test from inside the cluster:
kubectl run probe --image=curlimages/curl --rm -it --restart=Never -n vaultdms -- \
  curl -m 5 https://s3.us-east-1.amazonaws.com
# Expect: connection refused / timeout. If S3 us-east-1 responds,
# the NetworkPolicy isn't being enforced — investigate the CNI.

kubectl run probe --image=curlimages/curl --rm -it --restart=Never -n vaultdms -- \
  curl -m 5 https://s3.me-central-1.amazonaws.com
# Expect: 403 (anonymous request to S3) — proves egress to UAE works.
```

If your CNI doesn't support FQDN policies, fall back to CIDR-based
egress using the AWS IP ranges JSON
(<https://ip-ranges.amazonaws.com/ip-ranges.json>, filter
`region=me-central-1`).

---

## 8. CBUAE-acceptable encryption posture

The deployment satisfies the CBUAE TRA guidance for
financial-data residency:

| Requirement                                | How it's satisfied |
|--------------------------------------------|--------------------|
| Encryption at rest, FIPS 140-2 module      | AWS KMS HSM (FIPS 140-2 L3) for the KEK; per-tenant DEK wrapped by it. |
| Encryption in transit, TLS 1.2 minimum     | OpenSearch domain enforces TLS 1.2; ingress terminates at TLS 1.3. |
| Key custody                                | Per-tenant KEK, no cross-region replicas. Crypto-shred by `aws kms schedule-key-deletion` with the 7-day waiting period documented to customer. |
| Data sovereignty                           | NetworkPolicy `denyDefault: true` + AWS SCP denying non-me-central-1 endpoints. |
| Right of audit                             | All API access logged via the audit service; logs ship to a customer-owned S3 bucket in their own me-central-1 account on request (see §9). |

CBUAE alignment is necessary but not sufficient — production
deployments also need a customer-facing **Data Processing
Agreement** referencing this deployment topology. Talk to legal.

---

## 9. Sovereign-cloud option (Core42 / G42)

Customers with a Tier-1 CBUAE classification will require
deployment on a UAE-sovereign cloud rather than AWS. Core42's
G42 Cloud offers an S3-compatible object store + KMS-equivalent
HSM. The Helm overlay's AWS-specific bits are:

- `global.s3.endpoint` — point at G42's S3 endpoint URL
- `global.kms.provider` — switch from `aws` to `g42` (requires the
  G42 client wrapper in `pkg/crypto/g42.go`; not built yet)
- `global.opensearch.host` — G42 offers managed OpenSearch as a
  region-resident service

Everything else (the middleware, the residency guard, the
NetworkPolicy) ships verbatim. Phase 2 builds the G42 KMS
wrapper; this runbook documents the pattern but does NOT exercise
it.

---

## 10. Arabic-language production support SLA

A UAE deployment that ships the i18n foundation (ADRs 0106-0108)
implies a production-support commitment in Arabic. Two paths:

1. **Bilingual support pod** — existing on-call rotation extended
   with engineers fluent in Arabic. Pages route via PagerDuty's
   schedule overrides during MENA business hours.
2. **Tier-1 partner** — local Dubai-based MSP fronts L1/L2 calls
   in Arabic, escalates to our English-speaking on-call for L3+.

Spelled out in the customer's MSA — choose at contract-signing.

---

## 11. Confirming the residency claim end-to-end

After deployment, drive these three checks from a customer-witness
seat:

```sh
# 1. Provision a test tenant. The cluster will refuse anything
#    except primary_region=uae-central.
curl -X POST https://vaultdms.ae/api/v1/admin/tenants \
  -d '{"org_name":"Test","plan":"standard","region":"us-east-1"}'
# Expect 400 — "residency mismatch — tenant requested us-east-1
# but cluster serves uae-central"

curl -X POST https://vaultdms.ae/api/v1/admin/tenants \
  -d '{"org_name":"Test","plan":"standard","region":"uae-central"}'
# Expect 200 + a tenant_id.

# 2. Confirm /healthz reports the right region.
curl https://vaultdms.ae/healthz
# Expect: {"status":"alive","service":"document","region":"uae-central"}

# 3. From a US workstation, attempt a federated platform-admin
#    search against the UAE cluster. Expect 451 + the
#    X-DMS-Region-Block header.
```

Print these three exchanges. Sign + date them. That's the
residency-attestation artifact the buyer files with their
auditor.
