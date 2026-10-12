variable "region" {
  description = "AWS region for the load-test cluster. Must differ from prod's region OR use a separate AWS account."
  type        = string
  default     = "us-east-1"
}

variable "cluster_name" {
  description = "EKS cluster name. Must contain 'loadtest' — the prod IAM deny policy keys on this substring."
  type        = string
  default     = "vaultdms-loadtest"

  validation {
    condition     = can(regex("loadtest", var.cluster_name))
    error_message = "cluster_name MUST contain 'loadtest' so prod's deny-policy can fence us off."
  }
}

variable "kubernetes_version" {
  description = "EKS Kubernetes version."
  type        = string
  default     = "1.30"
}

variable "vpc_id" {
  description = "VPC the load-test cluster runs in. Provision via deploy/load-test/terraform/network.tf.example before applying this root module."
  type        = string
}

variable "private_subnet_ids" {
  description = "Private subnet IDs in the load-test VPC."
  type        = list(string)
}

variable "endpoint_public_access" {
  description = "Expose the EKS API server publicly. Off by default: reach the private endpoint from inside the VPC. When true, endpoint_public_access_cidrs must list the operator CIDRs."
  type        = bool
  default     = false
}

variable "endpoint_public_access_cidrs" {
  description = "CIDRs allowed to reach the public EKS endpoint when endpoint_public_access = true. 0.0.0.0/0 is rejected."
  type        = list(string)
  default     = []

  validation {
    condition     = !contains(var.endpoint_public_access_cidrs, "0.0.0.0/0") && !contains(var.endpoint_public_access_cidrs, "::/0")
    error_message = "endpoint_public_access_cidrs must not contain 0.0.0.0/0 or ::/0; list the operator egress CIDRs."
  }

  # An empty list on a public endpoint makes EKS default to 0.0.0.0/0.
  validation {
    condition     = !var.endpoint_public_access || length(var.endpoint_public_access_cidrs) > 0
    error_message = "endpoint_public_access = true requires at least one CIDR in endpoint_public_access_cidrs."
  }
}

variable "control_node_count" {
  description = "Size of the control-plane node group (gateway/auth/policy/notification pods)."
  type        = number
  default     = 4
}

variable "data_node_count" {
  description = "Size of the data-plane node group. Sized for ~70% headroom under §16 load so chaos pod-kills don't cliff."
  type        = number
  default     = 8
}

variable "loadrunner_node_count" {
  description = "Size of the loadrunner node group. §16 calls for ≥50 vCPUs; 8 × c6i.2xlarge = 64 vCPUs."
  type        = number
  default     = 8
}
