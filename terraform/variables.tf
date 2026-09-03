variable "aws_region" {
  description = "AWS region to deploy resources into"
  type        = string
  default     = "ap-northeast-1"
}

variable "project_name" {
  description = "Prefix used for resource naming"
  type        = string
  default     = "cloudfront-signed"
}

variable "bucket_name" {
  description = "S3 bucket name. When null, a globally unique name is generated from the account ID"
  type        = string
  default     = null
}

variable "public_key_path" {
  description = "Path to the CloudFront public key PEM file. Only the public key is read; the private key never passes through Terraform"
  type        = string
  default     = "../keys/public_key.pem"
}

variable "secret_name" {
  description = "Name of the Secrets Manager secret that will hold the private key (value is populated outside Terraform)"
  type        = string
  default     = "cloudfront-signed/private-key"
}

variable "cloudfront_price_class" {
  description = "CloudFront price class"
  type        = string
  default     = "PriceClass_100"
}

variable "tags" {
  description = "Common tags applied to all resources"
  type        = map(string)
  default = {
    Project   = "cloudfront-signed"
    ManagedBy = "terraform"
  }
}
