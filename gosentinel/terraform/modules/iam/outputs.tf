output "irsa_role_arns" {
  value = { for k, v in aws_iam_role.irsa : k => v.arn }
}
