variable "cluster_name"          { type = string }
variable "kubernetes_version"    { type = string; default = "1.29" }
variable "vpc_id"                { type = string }
variable "private_subnet_ids"    { type = list(string) }
variable "public_subnet_ids"     { type = list(string) }
variable "node_instance_types"   { type = list(string); default = ["m5.xlarge"] }
variable "capacity_type"         { type = string; default = "ON_DEMAND" }
variable "desired_nodes"         { type = number; default = 3 }
variable "min_nodes"             { type = number; default = 2 }
variable "max_nodes"             { type = number; default = 10 }
variable "endpoint_public_access" { type = bool; default = true }
variable "public_access_cidrs"   { type = list(string); default = ["0.0.0.0/0"] }
variable "tags"                  { type = map(string); default = {} }
