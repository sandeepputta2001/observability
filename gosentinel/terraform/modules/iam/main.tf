################################################################################
# IAM Module — IRSA roles for GoSentinel workloads
# Implements least-privilege IRSA (IAM Roles for Service Accounts) pattern.
################################################################################

terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 5.0" }
  }
}

data "aws_caller_identity" "current" {}

locals {
  oidc_host = replace(var.oidc_issuer_url, "https://", "")
}

# ── Helper: IRSA assume-role policy factory ───────────────────────────────────
data "aws_iam_policy_document" "irsa_assume" {
  for_each = var.service_accounts

  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    effect  = "Allow"
    principals {
      type        = "Federated"
      identifiers = [var.oidc_provider_arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.oidc_host}:sub"
      values   = ["system:serviceaccount:${each.value.namespace}:${each.value.name}"]
    }
    condition {
      test     = "StringEquals"
      variable = "${local.oidc_host}:aud"
      values   = ["sts.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "irsa" {
  for_each           = var.service_accounts
  name               = "${var.cluster_name}-${each.key}-irsa"
  assume_role_policy = data.aws_iam_policy_document.irsa_assume[each.key].json
  tags               = var.tags
}

# ── GoSentinel Pipeline — Secrets Manager read access ────────────────────────
resource "aws_iam_policy" "pipeline_secrets" {
  name = "${var.cluster_name}-pipeline-secrets-policy"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret"]
        Resource = "arn:aws:secretsmanager:*:${data.aws_caller_identity.current.account_id}:secret:${var.cluster_name}-*"
      },
      {
        Effect   = "Allow"
        Action   = ["kms:Decrypt"]
        Resource = var.kms_key_arn
      }
    ]
  })
  tags = var.tags
}

resource "aws_iam_role_policy_attachment" "pipeline_secrets" {
  count      = contains(keys(var.service_accounts), "pipeline") ? 1 : 0
  policy_arn = aws_iam_policy.pipeline_secrets.arn
  role       = aws_iam_role.irsa["pipeline"].name
}

# ── CloudWatch metrics write (for AWS Container Insights) ────────────────────
resource "aws_iam_policy" "cloudwatch_metrics" {
  name = "${var.cluster_name}-cloudwatch-metrics-policy"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["cloudwatch:PutMetricData", "ec2:DescribeVolumes", "ec2:DescribeTags", "logs:PutLogEvents", "logs:DescribeLogStreams", "logs:DescribeLogGroups", "logs:CreateLogStream", "logs:CreateLogGroup"]
      Resource = "*"
    }]
  })
  tags = var.tags
}
