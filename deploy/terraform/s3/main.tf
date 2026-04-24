# Wave 15 / Phase 1.6 — AWS S3 bucket topology for VaultDMS prod + staging.
#
# 5 buckets per deployment; 1 KMS CMK per deployment; 1 IAM role for
# IRSA. Apply once per environment (staging, prod). Module inputs
# parameterise region + environment + web origin for CORS.
#
# Bucket naming: dms-${environment}-${purpose}. Must be
# globally-unique; the environment prefix handles that at the
# customer-account level.

terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.0" }
  }
}

variable "environment" {
  description = "staging | prod"
  type        = string
}

variable "region" {
  description = "AWS region (e.g. us-east-1)"
  type        = string
}

variable "web_origin" {
  description = "Web UI origin for CORS (e.g. https://app.vaultdms.example)"
  type        = string
}

variable "cluster_oidc_provider_arn" {
  description = "EKS cluster OIDC provider ARN for IRSA"
  type        = string
}

variable "namespace" {
  description = "K8s namespace the IRSA role binds to"
  type        = string
  default     = "vaultdms"
}

# ---- KMS CMK --------------------------------------------------------------

resource "aws_kms_key" "vaultdms" {
  description             = "VaultDMS ${var.environment} — SSE-KMS for S3 + per-tenant KEK wrap"
  deletion_window_in_days = 30
  enable_key_rotation     = true
  tags = {
    Environment = var.environment
    Service     = "vaultdms"
  }
}

resource "aws_kms_alias" "vaultdms" {
  name          = "alias/vaultdms-${var.environment}"
  target_key_id = aws_kms_key.vaultdms.key_id
}

# ---- Buckets --------------------------------------------------------------

locals {
  bucket_purposes = ["objects", "previews", "exports", "dsr", "backups"]
}

resource "aws_s3_bucket" "bucket" {
  for_each = toset(local.bucket_purposes)
  bucket   = "dms-${var.environment}-${each.key}"
  tags = {
    Environment = var.environment
    Purpose     = each.key
    Service     = "vaultdms"
  }
}

# Block ALL public access on every bucket. Four flags; if any is
# false, the bucket can leak. Left explicit so an audit can grep
# these names.
resource "aws_s3_bucket_public_access_block" "pab" {
  for_each                = aws_s3_bucket.bucket
  bucket                  = each.value.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "versioning" {
  for_each = aws_s3_bucket.bucket
  bucket   = each.value.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "sse" {
  for_each = aws_s3_bucket.bucket
  bucket   = each.value.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.vaultdms.arn
    }
    bucket_key_enabled = true
  }
}

# Abort incomplete multipart uploads after 7 days. Critical for the
# storage service's presigned-PUT flow — client crashes between
# initiate and complete would otherwise accrue $$ in partial uploads.
resource "aws_s3_bucket_lifecycle_configuration" "lifecycle" {
  for_each = aws_s3_bucket.bucket
  bucket   = each.value.id
  rule {
    id     = "abort-incomplete-multipart"
    status = "Enabled"
    abort_incomplete_multipart_upload { days_after_initiation = 7 }
    filter {}
  }
}

# CORS on the objects bucket only — direct PUT/GET from the browser
# on presigned URLs. Other buckets are backend-only.
resource "aws_s3_bucket_cors_configuration" "cors" {
  bucket = aws_s3_bucket.bucket["objects"].id
  cors_rule {
    allowed_methods = ["PUT", "GET", "HEAD"]
    allowed_origins = [var.web_origin]
    allowed_headers = ["*"]
    expose_headers  = ["ETag"]
    max_age_seconds = 3000
  }
}

# ---- IRSA role + policy ---------------------------------------------------

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [var.cluster_oidc_provider_arn]
    }
    condition {
      test     = "StringEquals"
      variable = replace(var.cluster_oidc_provider_arn, "arn:aws:iam::[^:]+:oidc-provider/", "") == "" ? "" : "${replace(var.cluster_oidc_provider_arn, "arn:aws:iam::[^:]+:oidc-provider/", "")}:sub"
      values   = ["system:serviceaccount:${var.namespace}:vaultdms-storage"]
    }
  }
}

data "aws_iam_policy_document" "s3_access" {
  # Least-privilege: per-bucket ARN enumeration, no wildcard.
  statement {
    actions = [
      "s3:GetObject", "s3:PutObject", "s3:DeleteObject",
      "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts",
    ]
    resources = [for b in aws_s3_bucket.bucket : "${b.arn}/*"]
  }
  statement {
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = [for b in aws_s3_bucket.bucket : b.arn]
  }
  statement {
    actions   = ["kms:Encrypt", "kms:Decrypt", "kms:GenerateDataKey"]
    resources = [aws_kms_key.vaultdms.arn]
  }
}

resource "aws_iam_role" "storage" {
  name               = "vaultdms-${var.environment}-storage"
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

resource "aws_iam_role_policy" "storage" {
  name   = "s3-access"
  role   = aws_iam_role.storage.id
  policy = data.aws_iam_policy_document.s3_access.json
}

# ---- Outputs --------------------------------------------------------------

output "bucket_names" {
  value = { for p, b in aws_s3_bucket.bucket : p => b.id }
}

output "kms_key_arn" {
  value = aws_kms_key.vaultdms.arn
}

output "irsa_role_arn" {
  description = "Bind to the storage-service ServiceAccount via `eks.amazonaws.com/role-arn` annotation"
  value       = aws_iam_role.storage.arn
}
