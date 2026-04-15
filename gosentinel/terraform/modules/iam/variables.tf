variable "cluster_name"      { type = string }
variable "oidc_issuer_url"   { type = string }
variable "oidc_provider_arn" { type = string }
variable "kms_key_arn"       { type = string; default = "*" }
variable "service_accounts" {
  type = map(object({ namespace = string; name = string }))
  default = {
    pipeline = { namespace = "gosentinel", name = "gosentinel-pipeline" }
    api      = { namespace = "gosentinel", name = "gosentinel-api" }
    ui       = { namespace = "gosentinel", name = "gosentinel-ui" }
  }
}
variable "tags" { type = map(string); default = {} }
