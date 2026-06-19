terraform {
  backend "s3" {
    bucket       = "truth-in-stream-tfstate"
    key          = "main-account/terraform.tfstate"
    region       = "eu-west-3"
    use_lockfile = true
    encrypt      = true
  }
}
