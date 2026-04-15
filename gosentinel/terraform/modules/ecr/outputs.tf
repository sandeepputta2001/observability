output "repository_urls" {
  value = { for k, v in aws_ecr_repository.this : k => v.repository_url }
}
output "registry_id" {
  value = values(aws_ecr_repository.this)[0].registry_id
}
