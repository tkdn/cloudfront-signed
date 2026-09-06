# アップロード〜配信シーケンス

`cmd/upload-server`が実装するGitHub/esa.io型のアップロードフロー（S3 POST Policy発行 → ブラウザから直接S3へPOST → 完了通知 → CloudFront Signed URLへのリダイレクト）の全体シーケンス。

## 全体フロー

```mermaid
sequenceDiagram
    actor User as ブラウザ(web/index.html)
    participant Server as upload-server
    participant S3
    participant CloudFront

    Note over User,Server: 1. アップロードポリシー発行
    User->>Server: POST /api/upload/policies<br/>{userId, contentType, size}<br/>X-Upload-Secret
    Server->>Server: newObjectKey(userId, contentType)<br/>→ id (S3オブジェクトキーと兼用)
    Server->>S3: PresignPostObject(bucket, id, contentType, size)
    S3-->>Server: POST Policy (form.url, form.fields)
    Server->>Server: assetStore.Create(id, ...)<br/>newConfirmToken(id, expiresAt)
    Server-->>User: 200 {id, confirmToken, form}

    Note over User,S3: 2. S3への直接アップロード
    User->>S3: POST form.url<br/>FormData(form.fields + file)<br/>(サーバーを経由しない)
    S3-->>User: 204 No Content

    Note over User,Server: 3. 完了通知
    User->>Server: PATCH /api/upload/assets/{id}<br/>{confirmToken}<br/>X-Upload-Secret
    Server->>Server: verifyConfirmToken(id, confirmToken)
    Server->>Server: assetStore.Get(id)
    Server->>S3: HeadObject(bucket, id)
    S3-->>Server: 実体確認OK
    Server->>Server: assetStore.Confirm(id, now)<br/>(冪等: 既に確定済みなら何もしない)
    Server-->>User: 200 OK

    Note over User,CloudFront: 4. 確定済みアセットの表示
    User->>Server: GET /assets/{id}<br/>(img要素のsrcとして自動発火)
    Server->>Server: assetStore.Get(id)<br/>ConfirmedAt != nil を確認
    Server->>CloudFront: SignDownloadURL(id)<br/>(Custom Policy, DateLessThan)
    CloudFront-->>Server: 署名付きURL
    Server-->>User: 302 Redirect (Location: 署名付きURL)
    User->>CloudFront: GET 署名付きURL (自動追跡)
    CloudFront-->>User: 200 画像データ
```

## 異常系

### アップロードポリシー発行時のエラー

```mermaid
sequenceDiagram
    actor User as ブラウザ
    participant Server as upload-server
    participant S3

    User->>Server: POST /api/upload/policies
    alt X-Upload-Secret不一致
        Server-->>User: 401 Unauthorized
    else size <= 0
        Server-->>User: 400 Bad Request
    else contentTypeがimage/以外、または拡張子を解決できない
        Server-->>User: 400 Bad Request
    else id衝突（乱数の偶発的重複）
        Server->>Server: assetStore.Create(id, ...) → errAssetAlreadyExists
        Server-->>User: 409 Conflict
    else S3側の署名発行エラー
        Server->>S3: PresignPostObject(...)
        S3-->>Server: エラー
        Server-->>User: 500 Internal Server Error
    end
```

### 完了通知時のエラー

```mermaid
sequenceDiagram
    actor User as ブラウザ
    participant Server as upload-server
    participant S3

    User->>Server: PATCH /api/upload/assets/{id}
    alt X-Upload-Secret不一致
        Server-->>User: 401 Unauthorized
    else confirmTokenが不正・期限切れ
        Server->>Server: verifyConfirmToken(id, confirmToken) → false
        Server-->>User: 401 Unauthorized
        Note right of Server: assetStore.Getより先に検証するため、<br/>存在しないidでも同じ応答になる
    else レコードが存在しない
        Server->>Server: assetStore.Get(id) → errAssetNotFound
        Server-->>User: 404 Not Found
    else S3にオブジェクトが存在しない（アップロード未完了・失敗）
        Server->>S3: HeadObject(bucket, id)
        S3-->>Server: エラー
        Server-->>User: 404 Not Found
        Note right of Server: assetStore.Confirmは呼ばれず、<br/>ConfirmedAtはnilのまま維持される
    end
```

### アセット表示時のエラー

```mermaid
sequenceDiagram
    actor User as ブラウザ
    participant Server as upload-server
    participant CloudFront

    User->>Server: GET /assets/{id}
    alt レコードが存在しない
        Server->>Server: assetStore.Get(id) → errAssetNotFound
        Server-->>User: 404 Not Found
    else 完了通知がまだ行われていない（ConfirmedAt == nil）
        Server-->>User: 404 Not Found
    else CloudFront署名の発行に失敗
        Server->>CloudFront: SignDownloadURL(id)
        CloudFront-->>Server: エラー
        Server-->>User: 500 Internal Server Error
    end
```

## 各ステップの要点

### 1. アップロードポリシー発行 (`POST /api/upload/policies`)

- `id`はS3オブジェクトキーと兼用（`users/{userID}/{16バイトhex}{拡張子}`形式）。別途の内部IDは発行しない
- S3 POST Policyの`content-length-range`は申告`size`を最小=最大に固定し、`Content-Type`も完全一致条件で指定する（GitHub/esa.ioの実測分析に基づく設計。[S3アップロードAPIのレスポンス設計比較(GitHub/esa.io)](https://scrapbox.io/tkdn/S3アップロードAPIのレスポンス設計比較(GitHub/esa.io))参照）
- `confirmToken`はHMAC-SHA256によるステートレストークンで、`id`と有効期限のみをメッセージにする。レコードストアを参照せずに検証できる

### 2. S3への直接アップロード

- ブラウザは`fetch(form.url, { method: 'POST', body: formData })`でS3へ直接POSTする。`upload-server`はバイト列を一切中継しない
- このリクエストはContent-Typeヘッダーを明示指定しないため、ブラウザが自動生成する`multipart/form-data`がCORSのsimple content-typeに該当し、preflightは発生しない（[S3 POST PolicyアップロードでCORS preflightが発生しない理由](https://scrapbox.io/tkdn/S3_POST_PolicyアップロードでCORS_preflightが発生しない理由)参照）

### 3. 完了通知 (`PATCH /api/upload/assets/{id}`)

- `confirmToken`の検証はレコードストアへの問い合わせより先に行う（無効なトークンでストアの存在有無を露呈させないため）
- `HeadObject`でS3上の実体を同期的に確認してから`ConfirmedAt`を設定する。確認に失敗した場合は404を返し、レコードは未確定のまま
- `Confirm`は冪等: 既に確定済みのレコードへの再呼び出しは、最初の確定時刻を保持したまま成功を返す

### 4. 確定済みアセットの表示 (`GET /assets/{id}`)

- `<img src="/assets/{id}">`とするだけで、ブラウザが302リダイレクトを自動追跡しCloudFront署名付きURLから画像を取得する
- 未確定（`ConfirmedAt == nil`）のアセットは404を返す
- 検証目的のため認証・認可を行わず、`id`の推測困難性のみに依存している。実運用ではここでリクエスト元の認証とアクセス認可を必須にすること
