# CloudFront Signed URL 検証環境（Terraform）

## Context

CloudFront Signed URLの発行の仕組みを理解する目的で、Terraformを使ってAWSリソースを構築する。利用用途は「添付ファイルをユーザースコープで保護するCMS」を想定しており、事前のインタビューで以下が確定している。

- 署名方式はSigned URL（Signed Cookieではない）。ファイル単位のGETアクセス制御。
- アップロード（S3 Presigned URLでのPUT）は今回のスコープ外。将来別テーマとして検証する。
- ユーザースコープでのパス制限（`/users/{userId}/*`）はCustom Policyの`Resource`フィールドで署名時に行うアプリケーション側の責務とし、CloudFront側は単一のデフォルトビヘイビアで全パスを署名必須にする。
- 秘密鍵はTerraform外で`openssl genpkey`によりPKCS8形式で生成し、tfstateに一切含めない。公開鍵のみTerraformが読み込む。PKCS8を選ぶ理由は、署名検証に使うGoの`aws-sdk-go-v2/feature/cloudfront/sign`パッケージが提供する`sign.LoadPEMPrivKeyPKCS8AsSigner`がPKCS8形式のみを受け付けるため。`openssl genrsa`が出力するPKCS1形式を渡すとパースに失敗する（詳細は「Terraform外の手順」、および検証項目9を参照）。
- 実運用のベストプラクティスとしてSecrets Managerに秘密鍵を保存する構成を検証するが、シークレットの値投入はTerraform外（CLI）で行う。
- 独自ドメイン・ACM・Route53は対象外。デフォルトの`*.cloudfront.net`を使用。
- Terraform stateはローカル管理（使い捨て検証環境のため）。
- 署名生成の検証はGo（`github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign`）で行う。秘密鍵はPKCS8形式なので`LoadPEMPrivKeyPKCS8AsSigner`を使う想定。このGoスクリプト自体は今回のTerraform作業のスコープ外（将来作成）。

このプランはTerraformによるインフラ構築のみを対象とし、検証項目（インフラ正当性確認＋Go併用のE2E確認）まで含めて洗い出す。

## ファイル構成

モジュール化せず、フラットな単一ディレクトリ構成とする（リソース数が少なく、単一環境の検証用途のため）。AWSサービス単位でファイルを分割する。後日追加するGo署名スクリプトと分離するため、Terraform一式は`terraform/`配下にまとめる。

```
.
├── .gitignore                  # 既存。keys/private_key.pem を追加（公開鍵はコミット対象）
├── docs/
│   └── plan.md                 # このファイル
└── terraform/
    ├── providers.tf            # terraform{}, required_providers, provider "aws"
    ├── variables.tf            # 入力変数
    ├── s3.tf                   # S3バケット, Public Access Block, バケットポリシー
    ├── cloudfront.tf           # OAC, Public Key, Key Group, Distribution
    ├── secrets.tf              # aws_secretsmanager_secret（コンテナのみ）
    ├── outputs.tf              # 出力値
    └── terraform.tfvars.example # サンプル値（秘密情報を含まないためコミット対象）
```

鍵ペア（`keys/`）はTerraformとGo双方から参照するため、`terraform/`の外、リポジトリルート直下に置く（`public_key_path`のdefaultは`../keys/public_key.pem`に変更する）。

公開鍵（`keys/public_key.pem`）は秘匿情報ではなく、`aws_cloudfront_public_key`が`file()`で読み込む前提の入力ファイルであるため、コミット対象に含める。除外対象は秘密鍵（`keys/private_key.pem`）のみに絞る必要があり、`.gitignore`のパターンを`*.pem`や`keys/`ディレクトリ丸ごとではなく`keys/private_key.pem`のように限定する（後述の.gitignore節を参照）。

## リソース設計

### providers.tf
- `required_version = ">= 1.9.0"`、AWSプロバイダは`~> 6.0`（`terraform init`でロックファイルが実バージョンを確定する）。

  2026年9月3日時点でTerraform CLIの最新安定版はv1.16.1、AWSプロバイダの最新版はv6.62.0（7系は未リリース）。`~> 6.0`はpessimistic constraint（6.0以上7.0未満）として6.62.0を含む6.x系全体を正しくカバーしており妥当。一方`>= 1.9.0`は1.9系が2024年半ばのリリースで現行最新から7マイナーバージョン以上離れており、この計画が要求する特定の言語機能に基づく根拠はない（単に「動作する下限」として置いた値）。機能要件がなければこのままでも動作上の問題はないが、根拠を明確にする観点で`>= 1.12.0`程度に引き上げる選択肢もある。参照: https://github.com/hashicorp/terraform/releases 、 https://github.com/hashicorp/terraform-provider-aws/releases
- backendブロックは書かない（ローカルstateがデフォルト）。

### variables.tf
- `aws_region`（default `"ap-northeast-1"`）
- `project_name`（default `"cloudfront-signed"`、各リソースの命名プレフィックスに使用）
- `bucket_name`（未指定時は`data.aws_caller_identity.current.account_id`を使ってグローバル一意な名前を生成）
- `public_key_path`（default `"../keys/public_key.pem"`。**公開鍵のみ**を`file()`で読む。`keys/`は`terraform/`の外、リポジトリルート直下）
- `secret_name`（Secrets Managerのシークレット名）
- `cloudfront_price_class`（default `"PriceClass_100"`、コスト最小化。ユーザーの希望次第で変更可）
- キャッシュTTL関連変数、`tags`（共通タグmap）

秘密鍵を参照する変数は一切作らない。

### s3.tf
- `aws_s3_bucket`（新規作成）
- `aws_s3_bucket_public_access_block`（4項目すべて`true`）
- `aws_s3_bucket_policy` + `data.aws_iam_policy_document`: Principalを`cloudfront.amazonaws.com`とし、`Condition`の`AWS:SourceArn`で対象CloudFrontディストリビューションのARNに限定する（OACの標準パターン）。
- ACL・website configは設定しない（Block Public Accessと矛盾するため不要）。

### cloudfront.tf
- `aws_cloudfront_origin_access_control`（`signing_behavior = "always"`, `signing_protocol = "sigv4"`, `origin_access_control_origin_type = "s3"`）
- `aws_cloudfront_public_key`（`encoded_key = file(var.public_key_path)`）
- `aws_cloudfront_key_group`（`items = [aws_cloudfront_public_key.signer.id]`）
- `aws_cloudfront_distribution`
  - 単一origin（S3、OAC経由）
  - `default_cache_behavior`に`trusted_key_groups = [aws_cloudfront_key_group.signer.id]`を設定し、配下全パスを署名必須にする
  - `allowed_methods`/`cached_methods`は`["GET", "HEAD"]`のみ（アップロードはスコープ外のため）
  - キャッシュはAWS管理ポリシー`Managed-CachingOptimized`を使用（legacyな`forwarded_values`は使わない）
  - `viewer_certificate { cloudfront_default_certificate = true }`（独自ドメインなし）
  - `aliases`は設定しない

S3バケットポリシーがディストリビューションARNを参照し、ディストリビューションのoriginがバケットを参照するという相互参照になるが、Terraformの依存関係解決上は問題なく`apply`できる（循環にはならない）。

### secrets.tf
- `aws_secretsmanager_secret`のみ作成し、`aws_secretsmanager_secret_version`は作らない。シークレット値（秘密鍵PEM）はTerraform外でCLIから投入する。これにより秘密鍵の平文がtfstateに載ることを避ける。
- **今回の検証範囲にキーローテーションの自動化は含まない。** Secrets Manager標準のローテーション機能（Lambdaローテーション関数によるシークレット値の自動生成・更新）は、CloudFrontのPublic Key/Key Groupsと連動する仕組みを持たない汎用機能であり、秘密鍵PEMのような「Secrets Manager外のTerraformリソース（`aws_cloudfront_public_key`）と対になっている値」には標準ローテーションをそのまま適用できない。実運用でローテーションを行う場合は、(1)新しい鍵ペアを生成、(2)新しい公開鍵を`aws_cloudfront_public_key`として追加しKey Groupsに含める（旧鍵と併存させる）、(3)Secrets Managerの新バージョンとして新しい秘密鍵を投入、(4)署名生成側を新鍵に切り替え、(5)旧鍵の失効を確認してから`aws_cloudfront_public_key`を削除、という手動オーケストレーションが必要になる。この計画では検証項目8（公開鍵ローテーション後の旧鍵失効確認）でこの流れの一部のみを扱う。

### outputs.tf
- `cloudfront_domain_name`
- `cloudfront_distribution_id`
- `s3_bucket_name`
- `cloudfront_public_key_id`（Goスクリプトが署名時に使うKey Pair ID）
- `cloudfront_key_group_id`
- `secrets_manager_secret_name` / `secrets_manager_secret_arn`

### .gitignore 追加
既存のTerraform標準テンプレートに加え、以下を追加する。秘密鍵のみを除外し、公開鍵（`keys/public_key.pem`）はコミット対象に残す。

```
keys/private_key.pem
```

## Terraform外の手順（実行順）

1. PKCS8形式の鍵ペアをローカルに生成する。
   ```
   mkdir -p keys
   openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out keys/private_key.pem
   openssl rsa -pubout -in keys/private_key.pem -out keys/public_key.pem
   ```
   `openssl genpkey`はデフォルトでPKCS8形式を出力する（`openssl genrsa`が出力するPKCS1形式とは異なり、aws-sdk-go-v2の`LoadPEMPrivKeyPKCS8AsSigner`が期待する形式に対応）。`keys/`はリポジトリルート直下に作成する（`terraform/`ディレクトリの外）。生成後、`keys/public_key.pem`はコミットする（秘匿情報ではなく、`aws_cloudfront_public_key`が読み込む入力ファイルのため）。`keys/private_key.pem`のみ`.gitignore`で除外する。
2. `terraform/`ディレクトリに移動し、`terraform.tfvars.example`を`terraform.tfvars`にコピーして必要に応じて編集し、`terraform init && terraform plan && terraform apply`を実行する。
3. Secrets Managerにシークレット値を投入する。
   ```
   aws secretsmanager put-secret-value \
     --secret-id "$(terraform output -raw secrets_manager_secret_name)" \
     --secret-string file://keys/private_key.pem
   ```
4. `terraform output -raw cloudfront_public_key_id`と`cloudfront_domain_name`を控え、後日作成するGo署名スクリプトで使用する。

## 検証項目

このTerraform計画の完了時点で実際に検証できるのは、AWS CLIとcurlのみで確認可能な「インフラ正当性」の3項目のみ。Custom Policyでの正しい署名付きURL発行にはGo実装（このプランのスコープ外、将来作成）が必要なため、E2E項目とGo実装特有の確認項目は、あくまで「Go実装が完成した後に何を確認すべきか」の見通しとして記載するに留め、今回のTerraform作業では実施しない。

### インフラ正当性（Terraform apply後、AWS CLI/curlのみで確認できるもの。今回のスコープ内）
1. S3への直接アクセス（バケットのリージョナルエンドポイントに直接curl）が403になること（OAC＋バケットポリシーの効果）。
2. CloudFront経由でも署名なしアクセスは403になること（`trusted_key_groups`が効いていることの確認）。
3. ディストリビューションのorigin設定に`origin_access_control_id`が設定されており、旧OAIが使われていないことを確認する。

### E2E（Go署名スクリプト併用、将来のGo実装完了後に実施。今回はスコープ外）
4. Custom Policyで正しく発行した署名付きURLで、有効期限内・対象パス内であれば200でオブジェクトが取得できること。
5. 有効期限切れの署名付きURLで403（Expired系エラー）になること。
6. 署名対象と異なるパスに同じ署名クエリを流用した場合に403になること（Resourceパターンのスコープ確認）。
7. 署名クエリパラメータ（Signature/Policy等）を1文字改竄した場合に403になること。
8. 公開鍵をローテーション（新しい鍵ペアで`aws_cloudfront_public_key`を置き換え）した後、旧鍵で発行した署名付きURLが失効すること。

### Go実装特有の確認（同上、将来のGo実装完了後に実施。今回はスコープ外）
9. `LoadPEMPrivKeyPKCS8AsSigner`でPKCS8鍵が正しく読み込めること。PKCS1形式（`openssl genrsa`生成）を誤って渡した場合にエラーになることも合わせて確認する。
10. 生成された署名付きURLのクエリ文字列に、標準Base64の`+`, `/`, `=`が残っていないこと（CloudFront独自のBase64URL置換の確認）。
11. GoスクリプトがURLに埋め込む`Key-Pair-Id`が、`terraform output -raw cloudfront_public_key_id`と一致すること（鍵ローテーション後の不一致は典型的な実運用バグ）。

## 前提として明示するアサンプション

- `PriceClass_100`と`is_ipv6_enabled = true`は検証用途の低コスト構成としての既定値であり、インタビューで明示合意した項目ではない。変更希望があれば`variables.tf`で調整する。
- AWSプロバイダ`~> 6.0`は2026年9月3日時点の調査（最新v6.62.0、7系未リリース）で妥当性を確認済み。Terraform`>= 1.9.0`は「動作する下限」であり特定機能への依存に基づく根拠はない（詳細はproviders.tf節を参照）。いずれも`terraform init`時のロックファイルで実バージョンが確定する。
