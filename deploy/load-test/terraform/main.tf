# SeDoc load-test cluster — Terraform skeleton (ADR 0105 §16).
#
# This module provisions an ISOLATED load-test EKS cluster, distinct
# from prod. The cluster has three node groups:
#   - "control"     — the SUT control plane (gateway, auth, policy)
#   - "data"        — the data-plane services (document, storage, search)
#   - "loadrunner"  — k6-operator runners, sized for ≥50 vCPUs
#
# What this module does NOT do (operator responsibility):
#   - Provision the underlying VPC / subnets / IAM. We expect the
#     same module the prod terraform uses, but in a separate
#     environment. See deploy/load-test/terraform/network.tf.example.
#   - Apply the SeDoc Helm chart — that's a separate `helm upgrade`
#     step the runbook spells out, so the chart version is explicit
#     per campaign rather than baked in here.
#   - Seed the corpus — see tests/load/seed.py.
#
# Why a fresh cluster per campaign?
#   - Isolation: a misconfigured load test must NEVER paint prod
#     dashboards red. Different cluster = different Prometheus =
#     different alertmanager routes.
#   - Reproducibility: the campaign's terraform.tfvars file becomes a
#     historical record of "what infra produced these numbers."
#   - Teardown: when the campaign is over you `terraform destroy`
#     and the spend stops. No human has to remember to scale down.
#
# Apply protocol (operator only):
#   terraform init
#   terraform plan -var-file=campaigns/2026-05.tfvars -out=plan.tfplan
#   terraform apply plan.tfplan
#
# Tear down when finished:
#   terraform destroy -var-file=campaigns/2026-05.tfvars

terraform {
  required_version = ">= 1.5.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = ">= 2.23"
    }
    helm = {
      source  = "hashicorp/helm"
      version = ">= 2.11"
    }
  }

  # Use a load-test-only state bucket so a mistake here can NEVER
  # touch prod state. The bucket name MUST contain "loadtest"; the
  # CI workflow enforces this with a regex check before init.
  backend "s3" {
    bucket = "vaultdms-loadtest-tfstate"
    key    = "load-test/terraform.tfstate"
    region = "us-east-1"
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Environment = "loadtest"
      Purpose     = "blueprint-section-16"
      ManagedBy   = "terraform"
      ADR         = "0105"
      # Critical: this tag is what prod's "deny-touching-loadtest"
      # IAM policy keys on. Don't rename without coordinating.
      vaultdms_environment_class = "loadtest"
    }
  }
}

# --- EKS cluster ---------------------------------------------------

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 20.0"

  cluster_name    = var.cluster_name
  cluster_version = var.kubernetes_version

  vpc_id     = var.vpc_id
  subnet_ids = var.private_subnet_ids

  cluster_endpoint_public_access = true

  cluster_addons = {
    coredns                = { most_recent = true }
    kube-proxy             = { most_recent = true }
    vpc-cni                = { most_recent = true }
    aws-ebs-csi-driver     = { most_recent = true }
  }

  eks_managed_node_groups = {
    # The SUT's control-plane services (gateway, auth, policy,
    # notification). Smaller; latency-sensitive.
    control = {
      desired_size = var.control_node_count
      min_size     = 3
      max_size     = var.control_node_count * 2
      instance_types = ["c6i.2xlarge"]
      labels = { workload = "control" }
      taints = []
    }

    # Data-plane services + Postgres pods + OpenSearch + MinIO.
    # Heaviest; provision for ~70% headroom so the chaos overlay
    # doesn't fall off a cliff when we kill pods.
    data = {
      desired_size = var.data_node_count
      min_size     = 4
      max_size     = var.data_node_count * 2
      instance_types = ["m6i.4xlarge"]
      labels = { workload = "data" }
      taints = []
    }

    # Dedicated runner pool. §16 calls for ≥50 vCPUs; we default to
    # 8 × c6i.2xlarge = 64 vCPUs. Tainted so only k6 pods land here.
    loadrunner = {
      desired_size = var.loadrunner_node_count
      min_size     = 4
      max_size     = var.loadrunner_node_count * 2
      instance_types = ["c6i.2xlarge"]
      labels = { workload = "loadrunner" }
      taints = [{
        key    = "loadrunner"
        value  = "true"
        effect = "NO_SCHEDULE"
      }]
    }
  }
}

# --- k6 operator ---------------------------------------------------

# k6-operator lets us declare TestRuns as Kubernetes CRDs and runs
# them across the loadrunner node group. The runbook's `make
# run-protocol` target ultimately becomes a `kubectl apply -f
# testrun.yaml` against this cluster.
resource "helm_release" "k6_operator" {
  name             = "k6-operator"
  namespace        = "k6-operator-system"
  create_namespace = true
  repository       = "https://grafana.github.io/helm-charts"
  chart            = "k6-operator"
  version          = "3.6.0"

  set {
    name  = "nodeSelector.workload"
    value = "loadrunner"
  }
  set {
    name  = "tolerations[0].key"
    value = "loadrunner"
  }
  set {
    name  = "tolerations[0].operator"
    value = "Equal"
  }
  set {
    name  = "tolerations[0].value"
    value = "true"
  }
  set {
    name  = "tolerations[0].effect"
    value = "NoSchedule"
  }
}

# --- Prometheus + Grafana ------------------------------------------

# Pointed at the load-test cluster ONLY. Reuses the same dashboard
# JSON as the prod observability stack but with a different
# datasource UID so panels naturally separate.
resource "helm_release" "monitoring" {
  name             = "kube-prometheus-stack"
  namespace        = "monitoring"
  create_namespace = true
  repository       = "https://prometheus-community.github.io/helm-charts"
  chart            = "kube-prometheus-stack"
  version          = "60.0.0"

  values = [
    file("${path.module}/values/monitoring.yaml"),
  ]

  depends_on = [module.eks]
}
