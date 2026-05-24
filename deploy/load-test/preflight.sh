#!/usr/bin/env bash
# Load-test preflight — run BEFORE `terraform apply`. Fails fast on
# missing tooling, missing AWS perms, missing state bucket, or
# missing/invalid campaign tfvars. The terraform apply itself
# triggers ~$10/hour of cluster spend; finding out about a missing
# IAM permission 30 min in costs real money. This script costs zero.
#
# Referenced by docs/load-tests/RUNBOOK.md §0.
#
# Usage:
#   ./deploy/load-test/preflight.sh deploy/load-test/terraform/campaigns/2026-05.tfvars
#
# Exit codes:
#   0  all checks passed
#   1  required tooling missing
#   2  AWS access misconfigured
#   3  state bucket / network prerequisites missing
#   4  campaign tfvars missing / has CHANGE_ME placeholders

set -euo pipefail

# ----- colour --------------------------------------------------------
red()    { printf '\033[31m%s\033[0m\n' "$*"; }
green()  { printf '\033[32m%s\033[0m\n' "$*"; }
yellow() { printf '\033[33m%s\033[0m\n' "$*"; }
bold()   { printf '\033[1m%s\033[0m\n' "$*"; }

errors=0
warns=0
fail()  { red   "  ✗ $*"; errors=$((errors+1)); }
warn()  { yellow "  ! $*"; warns=$((warns+1)); }
ok()    { green "  ✓ $*"; }
step()  { echo; bold "▶ $*"; }

TFVARS="${1:-}"
if [[ -z "$TFVARS" ]]; then
  red "Usage: $0 <path-to-campaign-tfvars>"
  red "  e.g. $0 deploy/load-test/terraform/campaigns/2026-05.tfvars"
  exit 1
fi

# ----- 1. Tooling ----------------------------------------------------
step "1/5 Tooling"

need() {
  if command -v "$1" >/dev/null 2>&1; then
    local ver
    ver=$("$1" --version 2>&1 | head -1 || true)
    ok "$1 — $ver"
  else
    fail "$1 not installed. Install: $2"
  fi
}

need terraform "https://developer.hashicorp.com/terraform/downloads (>=1.5)"
need helm      "https://helm.sh/docs/intro/install/ (>=3.14)"
need kubectl   "https://kubernetes.io/docs/tasks/tools/ (>=1.30 client)"
need aws       "https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html"
need k6        "https://k6.io/docs/get-started/installation/ (>=0.51)"
need jq        "https://jqlang.github.io/jq/"

if (( errors > 0 )); then
  echo
  red "Tooling check failed — install the missing binaries and retry."
  exit 1
fi

# ----- 2. AWS access -------------------------------------------------
step "2/5 AWS access"

if ! ident=$(aws sts get-caller-identity 2>&1); then
  fail "aws sts get-caller-identity failed:"
  echo "$ident" | sed 's/^/      /'
  fail "Configure with 'aws configure' or set AWS_PROFILE / AWS_ACCESS_KEY_ID."
  exit 2
fi

account=$(echo "$ident" | jq -r '.Account')
arn=$(echo "$ident"     | jq -r '.Arn')
ok "Account: $account"
ok "Identity: $arn"

# Required IAM actions for the cluster stack. We can't fully simulate
# the policy without iam:SimulatePrincipalPolicy, but we can check
# the cheap actions and let `terraform plan` catch the rest.
required_actions=(
  "eks:CreateCluster"
  "eks:DescribeCluster"
  "ec2:CreateSecurityGroup"
  "ec2:DescribeSubnets"
  "iam:CreateRole"
  "iam:AttachRolePolicy"
  "s3:GetObject"
  "s3:PutObject"
)
if aws iam simulate-principal-policy \
     --policy-source-arn "$arn" \
     --action-names "${required_actions[@]}" \
     --output json >/tmp/preflight-iam.json 2>/dev/null; then
  denied=$(jq -r '.EvaluationResults[]
              | select(.EvalDecision != "allowed")
              | "  \(.EvalActionName) → \(.EvalDecision)"' /tmp/preflight-iam.json)
  if [[ -n "$denied" ]]; then
    fail "Some required IAM actions are NOT allowed for $arn:"
    echo "$denied" | sed 's/^/  /'
    fail "Attach an admin-equivalent policy or use a dedicated load-test role."
  else
    ok "All ${#required_actions[@]} probed IAM actions allowed"
  fi
else
  warn "iam:SimulatePrincipalPolicy denied — can't auto-verify perms."
  warn "Proceeding; terraform apply will surface missing perms (expensive)."
fi

# ----- 3. State bucket ----------------------------------------------
step "3/5 Terraform state bucket"

bucket="vaultdms-loadtest-tfstate"
if aws s3api head-bucket --bucket "$bucket" 2>/dev/null; then
  ok "Bucket s3://$bucket exists"
  ver=$(aws s3api get-bucket-versioning --bucket "$bucket" --query Status --output text 2>/dev/null || echo "")
  if [[ "$ver" == "Enabled" ]]; then
    ok "Versioning: Enabled"
  else
    warn "Versioning not enabled — a tfstate corruption would be unrecoverable."
    warn "Enable: aws s3api put-bucket-versioning --bucket $bucket --versioning-configuration Status=Enabled"
  fi
else
  fail "State bucket s3://$bucket missing. Create with:"
  cat <<EOF
        aws s3api create-bucket --bucket $bucket --region us-east-1
        aws s3api put-bucket-versioning \\
          --bucket $bucket \\
          --versioning-configuration Status=Enabled
        aws s3api put-bucket-encryption \\
          --bucket $bucket \\
          --server-side-encryption-configuration '{
            "Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]
          }'
        aws s3api put-public-access-block \\
          --bucket $bucket \\
          --public-access-block-configuration \\
            BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
EOF
fi

# ----- 4. Campaign tfvars -------------------------------------------
step "4/5 Campaign tfvars ($TFVARS)"

if [[ ! -f "$TFVARS" ]]; then
  fail "Tfvars file not found."
  echo "  Copy the example and fill it in:"
  echo "    cp deploy/load-test/terraform/campaigns/2026-05.tfvars.example $TFVARS"
  echo "    \$EDITOR $TFVARS"
  exit 4
fi
ok "Tfvars exists"

if grep -q 'CHANGE_ME' "$TFVARS"; then
  fail "Tfvars still has CHANGE_ME placeholders:"
  grep -n 'CHANGE_ME' "$TFVARS" | sed 's/^/      /'
  fail "Fill these from your network stack outputs:"
  echo  "      cd deploy/load-test/terraform && terraform output -raw vpc_id"
  echo  "      cd deploy/load-test/terraform && terraform output -json private_subnet_ids"
else
  ok "No CHANGE_ME placeholders"
fi

# Extract vpc_id + verify it exists in AWS.
vpc_id=$(grep -E '^[[:space:]]*vpc_id' "$TFVARS" | sed -E 's/.*"([^"]+)".*/\1/' || true)
if [[ -n "$vpc_id" && "$vpc_id" != "vpc-CHANGE_ME" ]]; then
  if aws ec2 describe-vpcs --vpc-ids "$vpc_id" --output text --query 'Vpcs[0].VpcId' >/dev/null 2>&1; then
    ok "VPC $vpc_id exists in AWS"
  else
    fail "VPC $vpc_id from tfvars NOT found in AWS account $account."
    fail "Did you run the network stack (deploy/load-test/terraform/network.tf.example)?"
  fi
fi

# ----- 5. Helm chart -------------------------------------------------
step "5/5 Helm chart"

chart_dir="$(cd "$(dirname "$0")"/../helm/vaultdms 2>/dev/null && pwd || true)"
if [[ -d "$chart_dir" && -f "$chart_dir/Chart.yaml" ]]; then
  ver=$(grep '^version:' "$chart_dir/Chart.yaml" | awk '{print $2}')
  ok "Chart present: $chart_dir (version $ver)"
  if helm lint "$chart_dir" >/dev/null 2>&1; then
    ok "helm lint: clean"
  else
    warn "helm lint reports issues — run 'helm lint $chart_dir' to see"
  fi
else
  fail "Helm chart not found at deploy/helm/vaultdms/"
  fail "Without the chart, RUNBOOK §1.4 (Helm install) can't proceed."
fi

# ----- summary ------------------------------------------------------
echo
bold "── Preflight summary ─────────────────────────────────────"
echo "  Errors:   $errors"
echo "  Warnings: $warns"
echo

if (( errors > 0 )); then
  red "PREFLIGHT FAILED — fix the errors above before terraform apply."
  red "Running terraform apply now would burn money on a broken setup."
  exit 1
fi

if (( warns > 0 )); then
  yellow "Preflight passed with $warns warning(s)."
  yellow "Review them; they won't block the apply but may bite during the run."
else
  green "PREFLIGHT PASSED — safe to proceed with terraform apply."
fi

echo
echo "Next steps (from RUNBOOK §1.2):"
echo "  cd deploy/load-test/terraform"
echo "  terraform init"
echo "  terraform plan  -var-file=$TFVARS -out=plan.tfplan"
echo "  terraform apply plan.tfplan"
