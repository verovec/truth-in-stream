data "aws_caller_identity" "current" {}

# Resources for the prod environment go here.
# Reusable modules live in ../modules and are referenced as:
#   module "networking" {
#     source = "../modules/networking"
#     ...
#   }
