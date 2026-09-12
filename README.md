# cloudfront-signed

CloudFront Signed URLの仕組みを学習目的で検証するリポジトリ。「添付ファイルをユーザースコープで保護するCMS」（github, esa.ioのような構成）を想定し、以下を手を動かして確認した。

## 検証したこと

1. **CloudFront Custom Policyによる署名付きURLの発行** — Canned Policyではなく、ユーザースコープのパス制限（`/users/{userId}/*`）をResourceフィールドで表現できるCustom Policyを採用
2. **GitHub/esa.io型のS3アップロードフロー** — S3 POST Policyを発行し、ブラウザがS3へ直接POSTする構成（サーバーは中継しない）で、完了通知・実体確認・CloudFront署名付きURLへのリダイレクトまでの一連のライフサイクルを確認（詳細は[docs/upload-sequence.md](docs/upload-sequence.md)のシーケンス図を参照）
3. **google/wireによるコンパイル時DI** — `cmd/upload-server`の依存解決（S3アダプタ・CloudFront署名器・アセットストア）を手動組み立てから`google/wire`の生成コードに置き換え、使い勝手を検証

## 構成

```
terraform/          CloudFront・S3・Secrets Managerの検証環境
cmd/sign-cli/        Custom Policyで署名付きURL(GET)を発行するCLI
cmd/upload-server/   S3 POST PolicyとCloudFront Signed URL(GET)を発行するHTTPサーバー
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

`cmd/upload-server`の設定はコマンドラインflagではなく環境変数（`UPLOAD_SERVER_`プレフィックス）で渡す。`.envrc`（[direnv](https://direnv.net/)）に以下のように書いておくと起動が楽になる。

```bash
export UPLOAD_SERVER_BUCKET="$(terraform -chdir=terraform output -raw s3_bucket_name)"
export UPLOAD_SERVER_CLOUDFRONT_DOMAIN="$(terraform -chdir=terraform output -raw cloudfront_domain_name)"
export UPLOAD_SERVER_KEY_PAIR_ID="$(terraform -chdir=terraform output -raw cloudfront_public_key_id)"
export UPLOAD_SERVER_PRIVATE_KEY="keys/private_key.pem"
export UPLOAD_SERVER_UPLOAD_SECRET="DONT_USE_THIS_CODE"
```

```bash
go run ./cmd/upload-server
```

`UPLOAD_SERVER_ADDR`（デフォルト`:8080`）と`UPLOAD_SERVER_EXPIRES`（デフォルト`15m`、署名付きURL・確認トークンの有効期限）は省略可能。必須の環境変数が不足している場合は、不足している変数名を列挙したエラーメッセージとともに終了する。

`http://localhost:8080/` をブラウザで開き、画像をアップロードすると閲覧用の署名付きURLが発行される。

> [!NOTE]
> `cmd/upload-server`は起動時に一度だけAWS認証情報を読み込み、プロセス生存期間中保持し続ける。STSの一時クレデンシャル（`aws login`等）が起動中に失効すると、S3へのPOSTが`403`（`ExpiredToken`または`InvalidAccessKeyId`）で失敗する。認証情報を更新したら、必ず`ps aux | grep upload-server`で古いプロセスが残っていないか確認してから再起動すること。

**AWSを使わずローカルモックで起動する:**

`UPLOAD_SERVER_MODE=mock`を指定すると、S3・CloudFrontへの実際のAWS呼び出しを一切行わず、ローカルファイルシステムへの保存・配信で同じAPIフロー（ポリシー発行→アップロード→確認→閲覧）を確認できる。AWS認証情報・CloudFront秘密鍵は不要。

```bash
export UPLOAD_SERVER_MODE="mock"
export UPLOAD_SERVER_BUCKET="mock-bucket"
export UPLOAD_SERVER_UPLOAD_SECRET="DONT_USE_THIS_CODE"
# 省略時は起動のたびに一時ディレクトリが作られる
export UPLOAD_SERVER_MOCK_STORAGE_DIR="tmp/mock-storage"

go run ./cmd/upload-server
```

`http://localhost:8080/`を開いて通常通りアップロード〜閲覧を試せる。アップロードされたファイルの実体は`UPLOAD_SERVER_MOCK_STORAGE_DIR`配下にオブジェクトキーと同じ相対パス（`users/{userId}/{ランダムID}.{拡張子}`）で保存される。

---

設計判断の詳細は `docs/` 配下、学習記録は [Cosense](https://scrapbox.io/tkdn/CloudFront_Signed_URL_発行CLIをGoで実装) を参照。
