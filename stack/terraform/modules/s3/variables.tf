variable "project" {
  type        = string
  description = "Project slug used in resource names."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "cors_allowed_origins" {
  type        = list(string)
  description = "Browser origins permitted to PUT/GET objects directly via presigned URLs (the frontend origin)."
}

variable "versioning_enabled" {
  type        = bool
  default     = false
  description = "Keep prior object versions. Off by default; large video makes versioning costly."
}

variable "expiration_days" {
  type        = number
  default     = 0
  description = "Days after which objects expire. 0 disables expiration (keep uploads indefinitely)."
}

variable "recordings_retention_days" {
  type        = number
  default     = 0
  description = "Days after which objects under the recordings/ prefix (TV capture archives) expire. 0 disables the prefix rule (the default): the app-level daily prune is authoritative and this is only a backstop. Intended for use with expiration_days=0. NOTE: expiration_days is whole-bucket and therefore also matches recordings/; when both are set, S3 applies the shorter of the two to recordings/, so keep recordings_retention_days <= expiration_days (or leave expiration_days at 0) to get the retention you configure here."
}
