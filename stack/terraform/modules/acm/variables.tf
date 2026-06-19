variable "domain_name" {
  type        = string
  description = "Primary domain the certificate covers (the apex, e.g. jeminforme.fr)."
}

variable "subject_alternative_names" {
  type        = list(string)
  default     = []
  description = "Additional names on the certificate (e.g. www.jeminforme.fr). The apex is set via domain_name and must not be repeated here."
}

variable "tags" {
  type        = map(string)
  default     = {}
  description = "Extra tags merged onto the certificate (provider default_tags still apply)."
}
