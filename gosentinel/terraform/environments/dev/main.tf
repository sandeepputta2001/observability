################################################################################
# GoSentinel — Dev Environment (smaller, cheaper, single-AZ)
################################################################################

terraform {
  required_version = ">= 1.7"
  required_providers {
    aws  = { source = "hashicorp/aws", version = "~> 5.0" }
    helm = { source = "hashicorp/helm", version = "~> 2.13" }
  }
  backend "s3" {
    bucket         = "gosentinel-terraform-state-dev"
    key            = "dev/terraform.tfstate"
    region         = "us-east-1"
    encrypt        = true
    dynamodb_table = "gosentinel-terraform-locks"
  }
}

provider "aws" {
  region = var.aws_region
  default_tags { tags = local.common_tags }
}

locals {
  cluster_name = "gosentinel-dev"
  common_tags = {
    Project     = "gosentinel"
    Environment = "dev"
    ManagedBy   = "terraform"
  }
}

module "vpc" {
  source       = "../../modules/vpc"
  name         = local.cluster_name
  vpc_cidr     = "10.1.0.0/16"
  cluster_name = local.cluster_name
  tags         = local.common_tags
}

module "eks" {
  source              = "../../modules/eks"
  cluster_name        = local.cluster_name
  vpc_id              = module.vpc.vpc_id
  private_subnet_ids  = module.vpc.private_subnet_ids
  public_subnet_ids   = module.vpc.public_subnet_ids
  node_instance_types = ["t3.large"]
  desired_nodes       = 2
  min_nodes           = 1
  max_nodes           = 4
  tags                = local.common_tags
}

module "ecr" {
  source = "../../modules/ecr"
  tags   = local.common_tags
}

variable "aws_region" { type = string; default = "us-east-1" }
