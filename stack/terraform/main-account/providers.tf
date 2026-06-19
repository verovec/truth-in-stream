// The default provider operates against the MAIN account (040265332493), which
// owns the authoritative hosted zone for jeminforme.fr. allowed_account_ids is a
// guard: terraform refuses to run if the resolved credentials are not the main
// account, so this root can never accidentally write DNS into the app account.
//
// Credentials resolve one of two ways:
//   - main_account_role_arn set -> assume that role (the cross-account path: the
//     operator authenticates with their own credentials and assumes a role in the
//     main account).
//   - main_account_role_arn empty -> use the ambient credentials directly (the
//     operator is already authenticated as the main account).
provider "aws" {
  region              = var.aws_region
  allowed_account_ids = [var.main_account_id]

  dynamic "assume_role" {
    for_each = var.main_account_role_arn == "" ? [] : [var.main_account_role_arn]
    content {
      role_arn     = assume_role.value
      session_name = "truth-in-stream-main-account-dns"
    }
  }

  default_tags {
    tags = {
      Project   = "truth-in-stream"
      Component = "main-account-dns"
      ManagedBy = "terraform"
    }
  }
}
