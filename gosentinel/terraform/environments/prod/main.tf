################################################################################
# GoSentinel — Production Environment
# Composes VPC + EKS + RDS + ECR + IAM modules.
################################################################################

terraform {
  required_version = ">= 1.7"

  required_providers {
    aws        = { source = "hashicorp/aws",        version = "~> 5.0" }
    kubernetes = { source = "hashicorp/kubernetes",  version = "~> 2.27" }
    helm       = { source = "hashicorp/helm",        version = "~> 2.13" }
  }

  backend "s3" {
    bucket         = "gosentinel-terraform-state-prod"
    key            = "prod/terraform.tfstate"
    region         = "us-east-1"
    encrypt        = true
    dynamodb_table = "gosentinel-terraform-locks"
  }
}

provider "aws" {
  region = var.aws_region
  default_tags { tags = local.common_tags }
}

provider "kubernetes" {
  host                   = module.eks.cluster_endpoint
  cluster_ca_certificate = base64decode(module.eks.cluster_ca_certificate)
  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "aws"
    args        = ["eks", "get-token", "--cluster-name", module.eks.cluster_name]
  }
}

provider "helm" {
  kubernetes {
    host                   = module.eks.cluster_endpoint
    cluster_ca_certificate = base64decode(module.eks.cluster_ca_certificate)
    exec {
      api_version = "client.authentication.k8s.io/v1beta1"
      command     = "aws"
      args        = ["eks", "get-token", "--cluster-name", module.eks.cluster_name]
    }
  }
}

locals {
  cluster_name = "gosentinel-prod"
  common_tags = {
    Project     = "gosentinel"
    Environment = "prod"
    ManagedBy   = "terraform"
    Owner       = var.owner
  }
}

# ── VPC ───────────────────────────────────────────────────────────────────────
module "vpc" {
  source       = "../../modules/vpc"
  name         = local.cluster_name
  vpc_cidr     = var.vpc_cidr
  cluster_name = local.cluster_name
  tags         = local.common_tags
}

# ── EKS ───────────────────────────────────────────────────────────────────────
module "eks" {
  source               = "../../modules/eks"
  cluster_name         = local.cluster_name
  kubernetes_version   = var.kubernetes_version
  vpc_id               = module.vpc.vpc_id
  private_subnet_ids   = module.vpc.private_subnet_ids
  public_subnet_ids    = module.vpc.public_subnet_ids
  node_instance_types  = var.node_instance_types
  desired_nodes        = var.desired_nodes
  min_nodes            = var.min_nodes
  max_nodes            = var.max_nodes
  endpoint_public_access = false  # private cluster in prod
  public_access_cidrs  = var.allowed_cidrs
  tags                 = local.common_tags
}

# ── RDS ───────────────────────────────────────────────────────────────────────
module "rds" {
  source                     = "../../modules/rds"
  identifier                 = "${local.cluster_name}-postgres"
  vpc_id                     = module.vpc.vpc_id
  subnet_ids                 = module.vpc.intra_subnet_ids
  allowed_security_group_ids = [module.eks.cluster_security_group_id]
  instance_class             = var.db_instance_class
  multi_az                   = true
  deletion_protection        = true
  tags                       = local.common_tags
}

# ── ECR ───────────────────────────────────────────────────────────────────────
module "ecr" {
  source = "../../modules/ecr"
  tags   = local.common_tags
}

# ── IAM / IRSA ────────────────────────────────────────────────────────────────
module "iam" {
  source            = "../../modules/iam"
  cluster_name      = local.cluster_name
  oidc_issuer_url   = module.eks.cluster_oidc_issuer_url
  oidc_provider_arn = module.eks.oidc_provider_arn
  tags              = local.common_tags
}

# ── Cluster Autoscaler (Helm) ─────────────────────────────────────────────────
resource "helm_release" "cluster_autoscaler" {
  name             = "cluster-autoscaler"
  repository       = "https://kubernetes.github.io/autoscaler"
  chart            = "cluster-autoscaler"
  version          = "9.36.0"
  namespace        = "kube-system"
  create_namespace = false

  set { name = "autoDiscovery.clusterName"; value = local.cluster_name }
  set { name = "awsRegion";                 value = var.aws_region }
  set { name = "rbac.serviceAccount.annotations.eks\\.amazonaws\\.com/role-arn"
        value = module.iam.irsa_role_arns["pipeline"] }

  depends_on = [module.eks]
}

# ── AWS Load Balancer Controller ──────────────────────────────────────────────
resource "helm_release" "aws_lbc" {
  name             = "aws-load-balancer-controller"
  repository       = "https://aws.github.io/eks-charts"
  chart            = "aws-load-balancer-controller"
  version          = "1.7.2"
  namespace        = "kube-system"
  create_namespace = false

  set { name = "clusterName"; value = local.cluster_name }
  set { name = "serviceAccount.annotations.eks\\.amazonaws\\.com/role-arn"
        value = module.iam.irsa_role_arns["api"] }

  depends_on = [module.eks]
}

# ── Metrics Server ────────────────────────────────────────────────────────────
resource "helm_release" "metrics_server" {
  name       = "metrics-server"
  repository = "https://kubernetes-sigs.github.io/metrics-server/"
  chart      = "metrics-server"
  version    = "3.12.1"
  namespace  = "kube-system"
  depends_on = [module.eks]
}

# ── GoSentinel Helm Release ───────────────────────────────────────────────────
resource "helm_release" "gosentinel" {
  name             = "gosentinel"
  chart            = "${path.module}/../../../deploy/helm/gosentinel"
  namespace        = "gosentinel"
  create_namespace = true

  values = [templatefile("${path.module}/helm-values.yaml.tpl", {
    pipeline_image = "${module.ecr.repository_urls["pipeline"]}:${var.image_tag}"
    api_image      = "${module.ecr.repository_urls["api"]}:${var.image_tag}"
    ui_image       = "${module.ecr.repository_urls["ui"]}:${var.image_tag}"
    db_secret_arn  = module.rds.db_secret_arn
    aws_region     = var.aws_region
  })]

  depends_on = [module.eks, module.rds, helm_release.metrics_server]
}
