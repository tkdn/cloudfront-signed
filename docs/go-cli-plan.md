# CloudFront Signed URL 発行CLI（Go）実装計画

## Context

`docs/terraform-plan.md`のTerraform計画は既にapply済みで、CloudFront・S3・Secrets Managerのインフラ検証(項目1〜3)は完了している。計画書内で「今回のTerraform作業のスコープ外、将来作成」と明示されていたGo署名スクリプトに今回着手する。

このCLIの役割は、[Custom Policy](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/private-content-creating-signed-url-custom-policy.html)でCloudFront Signed URLを発行することのみ。検証項目4〜11(有効期限内200確認、期限切れ403確認、パス流用403確認、改竄403確認、鍵ローテーション後の失効確認、PKCS8/PKCS1挙動、Base64URL置換、Key-Pair-Id一致)は、このCLIが出力したURLをユーザーがcurl等で手動確認する運用とする(自動テストとしては実装しない)。これはユーザーとの合意事項。

Custom Policyの比較対象は[Canned Policy](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/private-content-creating-signed-url-canned-policy.html)。Canned Policyは単一URLに対する有効期限のみを指定するシンプルな方式で、複数条件(IPアドレス制限や開始日時)やワイルドカードによるパス範囲指定はできない。今回はユーザースコープのパス制限(`/users/{userId}/*`のようなResourceパターン)を表現する必要があるため、複数の条件をStatement/Conditionとして組み立てられるCustom Policyを採用する(`docs/terraform-plan.md`のTerraform計画時点で確定済みの設計判断)。

## 確定した設計判断

- スコープ: 署名付きURL発行CLIのみ。自動検証は行わない。
- CLI引数: フラグ引数のみ(環境変数は使わない)。
- 署名方式: Custom Policy(`SignWithPolicy`)。Canned Policyは使わない。ユーザースコープのパス制限をResourceフィールドで表現する設計と整合させるため。
- 秘密鍵: PKCS8 PEMファイルをローカルパスで読む。Secrets Manager連携はこのCLIのスコープ外。
- モジュール配置: `go.mod`をリポジトリルート直下に置く。

## 使用SDK(実ソースで確認済み、v1.12.1が`go get`で取得される最新版)

パッケージ: `github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign`(go.modの`go`ディレクティブは1.24、ローカルGo 1.27.0でクリア)

```go
func LoadPEMPrivKeyPKCS8AsSigner(reader io.Reader) (crypto.Signer, error)

type URLSigner struct {
    HashAlg HashAlgorithm // 未設定時はSHA-1。今回は操作しない
}
func NewURLSigner(keyID string, signer crypto.Signer) *URLSigner
func (s URLSigner) SignWithPolicy(url string, p *Policy) (string, error)

type Condition struct {
    IPAddress       *IPAddress
    DateGreaterThan *AWSEpochTime
    DateLessThan    *AWSEpochTime // 必須
}
type Statement struct {
    Resource  string
    Condition Condition
}
type Policy struct {
    Statements []Statement `json:"Statement"`
}
func NewAWSEpochTime(t time.Time) *AWSEpochTime
```

JSON化・Base64URL置換(`+`→`-`, `/`→`_`, `=`→`_`)はSDK内部の`SignWithPolicy`が自動で行う。呼び出し側はPolicy構造体を組み立てるだけでよい。

`LoadPEMPrivKeyPKCS8AsSigner`にPKCS1形式を渡すと、内部の`x509.ParsePKCS8PrivateKey`が失敗し`"parse pkcs8 key: ..."`を含むエラーが返る。追加のフォーマット判定ロジックは不要(検証項目9の要求を自然に満たす)。

## 確定しているTerraform出力値(apply済み、実際の値。公開リポジトリのため本計画書上は伏せる)

- `cloudfront_public_key_id`: `terraform output -raw cloudfront_public_key_id`で取得する(以下`<KEY_PAIR_ID>`と表記)
- `cloudfront_domain_name`: `terraform output -raw cloudfront_domain_name`で取得する(以下`<CLOUDFRONT_DOMAIN>`と表記)
- 秘密鍵ファイル: `keys/private_key.pem`(PKCS8形式、ヘッダー`-----BEGIN PRIVATE KEY-----`を確認済み)

## ファイル構成

```
.
├── go.mod                    # 新規、モジュール名 github.com/tkdn/cloudfront-signed
├── go.sum                    # 新規
├── main.go                   # 新規、CLI本体(単一ファイル)
├── docs/
│   ├── terraform-plan.md      # docs/plan.mdからリネーム済み、実装完了後にGo実装への言及部分を更新
│   └── go-cli-plan.md         # このファイル
├── keys/                      # 既存
└── terraform/                 # 既存、変更なし
```

`go.mod`・`main.go`ともにリポジトリルート直下に置く。単一CLIなので`cmd/`によるサブディレクトリ分割は行わない(`go run .`で実行できる最小構成)。`internal/`パッケージへの分割も行わない — 実装量はフラグパース・鍵読み込み・Policy組み立て・Sign呼び出し・標準出力という一本道の手続きで、パッケージ分割するほどの独立した関心事がない。将来コマンドが増える場合はその時点で`cmd/`構成へ移行を検討する。

## CLIフラグ設計

| フラグ | 型 | 必須/デフォルト | 用途 |
|---|---|---|---|
| `-url` | string | 必須 | 実際に署名したいURL |
| `-resource` | string | 任意、未指定時は`-url`と同値 | Custom PolicyのResourceフィールド。`/users/alice/*`のようなワイルドカードを指定する場合に使う |
| `-key-pair-id` | string | 必須 | `terraform output -raw cloudfront_public_key_id`の値 |
| `-private-key` | string | 必須 | PKCS8 PEM秘密鍵のファイルパス |
| `-expires` | duration | デフォルト`1h` | 実行時刻からの署名有効期限オフセット(`time.ParseDuration`形式) |

IPAddress・DateGreaterThanはフラグ化しない(検証項目4〜11を自動化しない方針のため、要求されていないオプション条件は追加しない)。

## main関数の処理フロー

1. フラグをパースする
2. 必須フラグの空チェック、`-expires`が0以下でないことのチェック。失敗時は`os.Stderr`に出力し`os.Exit(1)`
3. `-resource`が空なら`-url`の値をコピーする
4. `os.Open(-private-key)`で秘密鍵ファイルを開く。失敗時はエラーを表示し終了
5. `sign.LoadPEMPrivKeyPKCS8AsSigner(f)`で`crypto.Signer`を取得。失敗時(PKCS1誤投入等)はSDKのエラーメッセージをそのまま表示して終了
6. `expiresAt := time.Now().Add(*expires)`を計算
7. `Policy{Statements: []Statement{{Resource: resource, Condition: Condition{DateLessThan: NewAWSEpochTime(expiresAt)}}}}`を組み立てる
8. `sign.NewURLSigner(*keyPairID, signer)`でURLSignerを生成
9. `signer.SignWithPolicy(*url, policy)`を呼ぶ。エラー時は表示して終了
10. 成功時は署名付きURLのみを標準出力に出す(付随情報を出すなら標準エラー出力に。シェルで`curl "$(go run . ...)"`のように扱えるようにする)

## エラーハンドリング方針

SDK/標準ライブラリが返すerrorをそのまま(必要なら`%w`でラップして文脈を付けて)表示し、非ゼロ終了する一本化した方針。独自のリトライ・フォールバック・事前フォーマット判定は行わない。

## 実装手順

1. `docs/plan.md`を`docs/terraform-plan.md`にリネームし、この計画を`docs/go-cli-plan.md`として格納する(Terraform出力値のプレースホルダー化を踏襲する)
2. `go mod init github.com/tkdn/cloudfront-signed`
3. `go get github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign@v1.12.1`
4. `main.go`を実装
5. `go build ./...` と `go vet ./...` で確認
6. 検証用オブジェクトをS3に配置した上で、`go run .`で署名付きURLを発行し、curlで動作確認(有効期限内で200が返ることを目視確認)
7. PKCS1形式(`openssl rsa -in keys/private_key.pem -out /tmp/pkcs1_test.pem`で一時生成、コミットしない)を`-private-key`に渡し、エラーで終了することを確認
8. 出力URLに標準Base64の`+`, `/`, `=`が残っていないこと、`Key-Pair-Id=<KEY_PAIR_ID>`(手順6で使った値と一致)が正しく埋め込まれていることを目視確認
9. `docs/terraform-plan.md`の該当箇所(Go署名スクリプトへの言及、検証項目9〜11)を実装完了に合わせて更新するかは、実装後に別途確認する

## 検証方法

署名付きURLで200を得るには、S3の対象パスにオブジェクトが実在している必要がある。Terraform計画側にはサンプルオブジェクトのアップロード手順を含めていないため、検証前に一度だけ手動で配置する。

```bash
# 事前準備: 検証用オブジェクトをS3に配置(1回だけ)
echo "hello" | aws s3 cp - "s3://$(terraform -chdir=terraform output -raw s3_bucket_name)/users/alice/sample.txt"

go build ./...
go vet ./...

go run . \
  -url "https://<CLOUDFRONT_DOMAIN>/users/alice/sample.txt" \
  -resource "https://<CLOUDFRONT_DOMAIN>/users/alice/*" \
  -key-pair-id "<KEY_PAIR_ID>" \
  -private-key "keys/private_key.pem" \
  -expires 1h
```

出力されたURLをcurlで叩き、200が返ることを確認する(オブジェクトを事前配置しているため404にはならないはず)。403が返る場合は署名やResourceパターンの不整合を疑う。
