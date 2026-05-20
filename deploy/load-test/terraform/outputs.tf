output "cluster_name" {
  description = "EKS cluster name — pipe into kubeconfig generation."
  value       = module.eks.cluster_name
}

output "cluster_endpoint" {
  description = "EKS API endpoint."
  value       = module.eks.cluster_endpoint
}

output "cluster_certificate_authority_data" {
  description = "CA bundle for kubeconfig."
  value       = module.eks.cluster_certificate_authority_data
  sensitive   = true
}

output "loadrunner_node_group_arn" {
  description = "ARN of the loadrunner node group — used by k6-operator's TestRun taint tolerations."
  value       = module.eks.eks_managed_node_groups["loadrunner"].node_group_arn
}

output "kubeconfig_command" {
  description = "One-liner to update your kubeconfig for this cluster."
  value       = "aws eks update-kubeconfig --region ${var.region} --name ${module.eks.cluster_name}"
}
