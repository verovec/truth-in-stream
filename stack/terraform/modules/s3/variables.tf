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
