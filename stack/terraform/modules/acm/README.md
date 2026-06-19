# modules/acm

Requests a public ACM certificate in **us-east-1** (the region required for
certificates fronting CloudFront) for an apex domain plus any SANs, using DNS
validation.

It deliberately does **not** create the DNS validation records and does **not**
include an `aws_acm_certificate_validation` resource. The authoritative hosted
zone for the domain lives in a **different AWS account** (the main account), so
the validation records are created there by a separate terraform root. This
module only requests the certificate and exposes the records that account must
create; the certificate stays `PENDING_VALIDATION` until they exist.

## Usage

The module is always driven by an aliased `us-east-1` provider:

```hcl
module "acm" {
  source = "../modules/acm"

  providers = {
    aws.us_east_1 = aws.us_east_1
  }

  domain_name               = "jeminforme.fr"
  subject_alternative_names = ["www.jeminforme.fr"]
}
```

## Inputs

| Name | Type | Default | Description |
|------|------|---------|-------------|
| `domain_name` | string | — | Apex/primary domain on the certificate. |
| `subject_alternative_names` | list(string) | `[]` | Extra names (e.g. `www`). Do not repeat the apex. |
| `tags` | map(string) | `{}` | Extra tags merged onto the certificate. |

## Outputs

| Name | Description |
|------|-------------|
| `certificate_arn` | ARN of the requested certificate (for CloudFront and the main-account root). |
| `domain_validation_options` | Map keyed by domain of `{ name, type, value }` CNAME records the authoritative zone must create. |

## Cross-account validation

The certificate reaches `ISSUED` only after the `domain_validation_options`
records exist in the authoritative hosted zone (in the main account). The
main-account terraform root reads this module's outputs (via remote state or
tfvars) and creates one CNAME per record. See the root `README.md`
("Cross-account ACM validation") for the end-to-end flow.
