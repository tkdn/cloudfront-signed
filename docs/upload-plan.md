# S3アップロード + CloudFront Signed URL 発行検証 実装計画

## Context

`docs/terraform-plan.md`・`docs/go-cli-plan.md`では、CloudFront Signed URLによる閲覧（GET）側の検証のみを行い、アップロード（S3へのPUT）は明示的にスコープ外としていた。本計画は、そのスコープ外だった「ユーザーがブラウザから画像をアップロードし、アップロード完了後にCloudFront Signed URLを受け取って閲覧する」という一連のフローを検証する。

grillingセッションで確定した設計判断は以下の通り。

- アップロードはS3 Presigned URL(PUT)方式を採用する。CloudFront経由のPUTは採用しない（PUTはキャッシュ対象にならずCDNの恩恵がなく、構成が複雑になるだけ）。
- 画像バイト列はブラウザ→S3へ直接流れる。バックエンドサーバーはPresigned URLの発行のみを行い、画像データを中継しない。
- アップロード完了検知は、ブラウザがS3へのPUT fetchのレスポンスstatusのみで判定する（2xxなら成功とみなす）。S3側の実在確認（HEADリクエスト等）は行わない。
- PUT用エンドポイントとGET用エンドポイントの間のキー受け渡しは、サーバーがPUT発行時にレスポンスボディで`key`を返し、ブラウザがそれをJS変数として保持してGETリクエストに含める。サーバー側でセッション状態は持たない。

## 確定した設計判断

- アップロード署名方式: S3 Presigned URL(PUT)。`github.com/aws/aws-sdk-go-v2/service/s3`の`PresignClient`を新規導入する。
- 発行者: 新規の軽量HTTPサーバー(`cmd/upload-server`)。既存の署名CLIとは別バイナリ。
- ディレクトリ構成: `docs/go-cli-plan.md`で「将来コマンドが増える場合は`cmd/`構成へ移行」と予告されていた通り、既存`main.go`を`cmd/sign-cli/main.go`に移動し、新規`cmd/upload-server/main.go`を追加する。
- ブラウザ実装: ビルドレスの単一HTMLファイル(`web/index.html`)。フレームワーク・ビルドステップなし。`cmd/upload-server`が静的ファイルとして配信する。
- Presigned URL発行エンドポイントの認証: 固定共有シークレットをリクエストヘッダー(`X-Upload-Secret`)で要求する。値自体が検証専用であることを示す文字列（例: `DONT_USE_THIS_CODE`）を使う。本番用の認証基盤ではないことをコメントで説明する必要はない（値自体が示している）。
- アップロードファイルサイズ制限: 設けない。検証用の小さい画像を使う運用で足りるため。
- S3オブジェクトキー: サーバー側で`users/{userId}/{uuid}.{拡張子}`形式を生成する。クライアントが任意のキーを指定できないようにする。
- Content-Type制限: `image/`プレフィックスを持つ値のみ許可する。
- CORS: S3バケットのCORS設定は、`cmd/upload-server`が配信する固定オリジンのみを許可する。`*`は使わない。
- 鍵・認証情報の入力: CloudFront秘密鍵は`cmd/sign-cli`と同様に`-private-key`フラグでPEMファイルパスを受け取り、サーバー起動時に一度だけ読み込んでプロセス生存期間中`crypto.Signer`として保持する。S3側のAWS認証情報は独自フラグを作らず、AWS SDKの標準的な認証情報チェーン(`config.LoadDefaultConfig`)に任せる。

## 使用SDK(実ソースで確認済み)

パッケージ: `github.com/aws/aws-sdk-go-v2/service/s3`(`go list -m -versions`で確認できる最新安定版 v1.111.0)

```go
func s3.NewFromConfig(cfg aws.Config, optFns ...func(*s3.Options)) *s3.Client
func s3.NewPresignClient(c *s3.Client, optFns ...func(*s3.PresignOptions)) *s3.PresignClient

func (c *s3.PresignClient) PresignPutObject(
    ctx context.Context,
    params *s3.PutObjectInput,
    optFns ...func(*s3.PresignOptions),
) (*v4.PresignedHTTPRequest, error) // v4 = github.com/aws/aws-sdk-go-v2/aws/signer/v4

func s3.WithPresignExpires(dur time.Duration) func(*s3.PresignOptions)

type s3.PutObjectInput struct {
    Bucket      *string
    Key         *string
    ContentType *string
    // ...
}

type v4.PresignedHTTPRequest struct {
    URL          string
    Method       string
    SignedHeader http.Header
}
```

`PresignPutObject`の有効期限は`s3.WithPresignExpires`未指定時デフォルト900秒(15分)。`s3.Client`の生成は標準フロー: `config.LoadDefaultConfig(ctx)` → `s3.NewFromConfig(cfg)` → `s3.NewPresignClient(client)`。

## ファイル構成

```
.
├── cmd/
│   ├── sign-cli/
│   │   └── main.go            # 既存main.goをそのまま移動（内容変更なし）
│   └── upload-server/
│       └── main.go            # 新規、HTTPサーバー本体
├── web/
│   └── index.html              # 新規、ビルドレスの単一HTMLファイル
├── docs/
│   ├── terraform-plan.md
│   ├── go-cli-plan.md
│   └── upload-plan.md          # このファイル
├── keys/                       # 既存、変更なし
└── terraform/
    ├── s3.tf                   # CORS設定・PUT許可ポリシーを追加
    └── ...                     # 他は変更なし
```

`go.mod`のmoduleパスは変更しない。`cmd/`配下への移動に伴い、既存の`go run .`は`go run ./cmd/sign-cli`に変わる。

## HTTPサーバー設計

### エンドポイント

| メソッド | パス | 用途 |
|---|---|---|
| `POST` | `/api/presign-upload` | S3 Presigned URL(PUT)を発行する |
| `POST` | `/api/presign-download` | 指定キーに対するCloudFront Signed URL(GET)を発行する |
| `GET` | `/` | `web/index.html`を配信する |

### `POST /api/presign-upload`

リクエスト:
- ヘッダー: `X-Upload-Secret: <共有シークレット>`（起動時フラグ`-upload-secret`の値と一致必須。不一致・欠落時は`401 Unauthorized`）
- ボディ(JSON): `{"userId": "alice", "contentType": "image/png"}`
  - `userId`が空、または`contentType`が`image/`で始まらない場合は`400 Bad Request`

レスポンス(JSON): `{"key": "users/alice/<uuid>.png", "uploadUrl": "https://..."}`

サーバー内部処理:
1. `X-Upload-Secret`ヘッダーを検証
2. リクエストボディをデコードし`userId`・`contentType`をバリデーション
3. `contentType`から拡張子を決定する（`mime.ExtensionsByType`を使う。候補が複数ある場合は先頭を採用）
4. `uuid.New()`でファイル名を生成し、`key = fmt.Sprintf("users/%s/%s%s", userId, uuid, ext)`を組み立てる
5. `presignClient.PresignPutObject`で`Bucket`・`Key`・`ContentType`を指定してPresigned URLを発行
6. `{"key": key, "uploadUrl": presignedURL}`をJSONで返す

### `POST /api/presign-download`

リクエスト:
- ヘッダー: `X-Upload-Secret: <共有シークレット>`(アップロード側と同じシークレットを流用する。閲覧側だけ認証なしにすると非対称になるため)
- ボディ(JSON): `{"key": "users/alice/<uuid>.png"}`

レスポンス(JSON): `{"downloadUrl": "https://<CLOUDFRONT_DOMAIN>/users/alice/<uuid>.png?Policy=...&Signature=...&Key-Pair-Id=..."}`

サーバー内部処理:
1. `X-Upload-Secret`ヘッダーを検証
2. `key`が空でないことを検証
3. `url := fmt.Sprintf("https://%s/%s", cloudfrontDomain, key)`を組み立てる
4. `resource := url`（ワイルドカードなし、発行したキー自身に限定する。`cmd/sign-cli`のようなユーザー指定の`-resource`フラグは持たない）
5. 既存の`sign.URLSigner.SignWithPolicy`で署名し、`{"downloadUrl": signedURL}`を返す

### CLIフラグ設計(`cmd/upload-server`)

| フラグ | 型 | 必須/デフォルト | 用途 |
|---|---|---|---|
| `-addr` | string | デフォルト`:8080` | HTTPサーバーのlisten address |
| `-bucket` | string | 必須 | アップロード先S3バケット名 |
| `-cloudfront-domain` | string | 必須 | Signed URL発行対象のCloudFrontドメイン |
| `-key-pair-id` | string | 必須 | `terraform output -raw cloudfront_public_key_id`の値 |
| `-private-key` | string | 必須 | PKCS8 PEM秘密鍵のファイルパス |
| `-upload-secret` | string | 必須 | Presigned URL発行エンドポイントを保護する共有シークレット |
| `-expires` | duration | デフォルト`15m` | ダウンロード用Signed URLの有効期限オフセット |

## Terraform変更

`terraform/s3.tf`に以下を追加する。

- `aws_s3_bucket_cors_configuration`: `allowed_origins`に`cmd/upload-server`の固定オリジン(`http://localhost:8080`)、`allowed_methods`に`["PUT"]`、`allowed_headers`に`["Content-Type"]`を指定する。
- 既存のバケットポリシー(OAC経由のCloudFrontからのGetObjectのみ許可)はそのまま維持する。PUTはCloudFrontを経由しないため、バケットポリシーの変更は不要（Presigned URLはSigV4署名によるIAM権限の一時委譲であり、バケットポリシーではなくIAM側の権限で許可される）。

## ブラウザ実装(`web/index.html`)

`<input type="file" accept="image/*">` → 選択時に`fetch('/api/presign-upload', {method: 'POST', headers: {'X-Upload-Secret': ..., 'Content-Type': 'application/json'}, body: JSON.stringify({userId, contentType: file.type})})` → レスポンスの`uploadUrl`へ`fetch(uploadUrl, {method: 'PUT', headers: {'Content-Type': file.type}, body: file})` → `response.ok`なら`key`を使って`/api/presign-download`を呼び、返ってきた`downloadUrl`を`<a>`タグで画面に表示する。

`userId`と共有シークレットは画面上の`<input>`で入力させる（ハードコードしない。検証時に複数ユーザーを模擬しやすくするため）。

## 検証方法

```bash
go build ./...
go vet ./...

go run ./cmd/upload-server \
  -bucket "$(terraform -chdir=terraform output -raw s3_bucket_name)" \
  -cloudfront-domain "$(terraform -chdir=terraform output -raw cloudfront_domain_name)" \
  -key-pair-id "$(terraform -chdir=terraform output -raw cloudfront_public_key_id)" \
  -private-key "keys/private_key.pem" \
  -upload-secret "DONT_USE_THIS_CODE"
```

ブラウザで`http://localhost:8080/`を開き、以下を確認する。

1. 画像ファイルを選択しアップロードする。S3への直接PUTが成功し(開発者ツールのNetworkタブでリクエスト先が`https://<bucket>.s3.<region>.amazonaws.com/...`であることを確認)、続けて取得したCloudFront Signed URLでGETし200が返ること。
2. `X-Upload-Secret`ヘッダーを外した状態（開発者ツールでリクエストを改変するか、`curl`で直接叩く）で`/api/presign-upload`を呼び、`401`が返ること。
3. ブラウザの開発者ツールで、`http://localhost:8080`以外のオリジン(例: `file://`から開いた別のHTMLファイル、または`https://example.com`を模したCORSプリフライト)からS3へのPUTを試み、CORSエラーでブロックされることを確認する。
4. `image/png`以外のContent-Type(例: `application/octet-stream`)を指定して`/api/presign-upload`を呼び、`400`が返ることを確認する。

## 実装手順

1. `main.go`を`cmd/sign-cli/main.go`に移動する
2. `go get github.com/aws/aws-sdk-go-v2/service/s3` と `go get github.com/google/uuid` を実行する
3. `terraform/s3.tf`にCORS設定を追加し、`terraform apply`する
4. `cmd/upload-server/main.go`を実装する
5. `web/index.html`を実装する
6. `go build ./...` と `go vet ./...` で確認する
7. 上記「検証方法」の1〜4を実施する
