variable "identifier"                  { type = string }
variable "vpc_id"                      { type = string }
variable "subnet_ids"                  { type = list(string) }
variable "allowed_security_group_ids"  { type = list(string) }
variable "db_name"                     { type = string; default = "gosentinel" }
variable "db_username"                 { type = string; default = "gosentinel" }
variable "instance_class"              { type = string; default = "db.t3.medium" }
variable "allocated_storage"           { type = number; default = 20 }
variable "max_allocated_storage"       { type = number; default = 100 }
variable "multi_az"                    { type = bool;   default = true }
variable "deletion_protection"         { type = bool;   default = true }
variable "tags"                        { type = map(string); default = {} }
