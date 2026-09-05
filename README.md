# cloudfront-signed

CloudFront Signed URLの仕組みを学習目的で検証するリポジトリ。「添付ファイルをユーザースコープで保護するCMS」（github, esa.ioのような構成）を想定し、以下を手を動かして確認した。

## 検証したこと

1. **CloudFront Custom Policyによる署名付きURLの発行** — Canned Policyではなく、ユーザースコープのパス制限（`/users/{userId}/*`）をResourceフィールドで表現できるCustom Policyを採用
2. **ブラウザからのS3直接アップロード** — S3 Presigned URL(PUT)を発行し、画像バイト列がブラウザ→S3へ直接流れる構成（サーバーは中継しない）を確認
3. **アップロード直後の閲覧フロー** — アップロード完了後にCloudFront Signed URL(GET)を発行し、実際に画像を閲覧できることを確認

## 構成

```
terraform/          CloudFront・S3・Secrets Managerの検証環境
cmd/sign-cli/        Custom Policyで署名付きURL(GET)を発行するCLI
cmd/upload-server/   S3 Presigned URL(PUT)とCloudFront Signed URL(GET)を発行するHTTPサーバー
web/index.html       ブラウザから動作確認するためのビルドレスUI
keys/                検証用鍵ペア（秘密鍵はコミット対象外）
docs/                各段階の設計判断・実装計画
```

## セットアップ

```bash
mkdir -p keys
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out keys/private_key.pem
openssl rsa -pubout -in keys/private_key.pem -out keys/public_key.pem

cd terraform
cp terraform.tfvars.example terraform.tfvars
terraform init && terraform apply

aws secretsmanager put-secret-value \
  --secret-id "$(terraform output -raw secrets_manager_secret_name)" \
  --secret-string file://../keys/private_key.pem
```

## 使い方

**署名付きURL(GET)をCLIで発行する:**

```bash
go run ./cmd/sign-cli \
  -url "https://$(terraform -chdir=terraform output -raw cloudfront_domain_name)/users/alice/sample.txt" \
  -key-pair-id "$(terraform -chdir=terraform output -raw cloudfront_public_key_id)" \
  -private-key keys/private_key.pem \
  -expires 1h
```

**アップロード検証サーバーを起動する:**

```bash
go run ./cmd/upload-server \
  -bucket "$(terraform -chdir=terraform output -raw s3_bucket_name)" \
  -cloudfront-domain "$(terraform -chdir=terraform output -raw cloudfront_domain_name)" \
  -key-pair-id "$(terraform -chdir=terraform output -raw cloudfront_public_key_id)" \
  -private-key keys/private_key.pem \
  -upload-secret "DONT_USE_THIS_CODE"
```

`http://localhost:8080/` をブラウザで開き、画像をアップロードすると閲覧用の署名付きURLが発行される。

> [!NOTE]
> `cmd/upload-server`は起動時に一度だけAWS認証情報を読み込み、プロセス生存期間中保持し続ける。STSの一時クレデンシャル（`aws login`等）が起動中に失効すると、S3へのPUTが`403 ExpiredToken`で失敗する。長時間サーバーを起動しっぱなしにしていた場合は、認証情報を更新してからサーバーを再起動すること。


---

設計判断の詳細は `docs/` 配下、学習記録は [Cosense](https://scrapbox.io/tkdn/CloudFront_Signed_URL_発行CLIをGoで実装) を参照。
