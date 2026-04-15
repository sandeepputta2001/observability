output "cluster_name"       { value = module.eks.cluster_name }
output "cluster_endpoint"   { value = module.eks.cluster_endpoint; sensitive = true }
output "ecr_urls"           { value = module.ecr.repository_urls }
output "db_secret_arn"      { value = module.rds.db_secret_arn }
output "kubeconfig_command" {
  value = "aws eks update-kubeconfig --region ${var.aws_region} --name ${module.eks.cluster_name}"
}
