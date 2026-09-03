output "cloudfront_domain_name" {
  description = "CloudFront distribution default domain name"
  value       = aws_cloudfront_distribution.this.domain_name
}

output "cloudfront_distribution_id" {
  description = "CloudFront distribution ID"
  value       = aws_cloudfront_distribution.this.id
}

output "s3_bucket_name" {
  description = "S3 origin bucket name"
  value       = aws_s3_bucket.origin.id
}

output "cloudfront_public_key_id" {
  description = "CloudFront Key Pair ID used when generating signed URLs"
  value       = aws_cloudfront_public_key.signer.id
}

output "cloudfront_key_group_id" {
  description = "CloudFront key group ID"
  value       = aws_cloudfront_key_group.signer.id
}

output "secrets_manager_secret_name" {
  description = "Secrets Manager secret name holding the private key"
  value       = aws_secretsmanager_secret.signing_key.name
}

output "secrets_manager_secret_arn" {
  description = "Secrets Manager secret ARN holding the private key"
  value       = aws_secretsmanager_secret.signing_key.arn
}
