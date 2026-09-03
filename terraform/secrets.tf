# Secret container only. The private key value is populated outside Terraform
# (see docs/plan.md "Terraform外の手順") so it never touches tfstate.
resource "aws_secretsmanager_secret" "signing_key" {
  name        = var.secret_name
  description = "CloudFront signed URL private key (PKCS8 PEM)"
}
