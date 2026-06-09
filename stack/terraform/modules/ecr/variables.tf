variable "project" {
  type        = string
  description = "Project slug used in resource names."
}

variable "environment" {
  type        = string
  description = "Deployment environment (dev, prod)."
}

variable "repositories" {
  type        = set(string)
  description = "Image names to create repositories for (e.g. backend, frontend, migrate)."
}

variable "keep_last_images" {
  type        = number
  default     = 10
  description = "How many tagged images to retain per repository."
}
