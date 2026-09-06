# アップロード〜配信シーケンス

`cmd/upload-server`が実装するGitHub/esa.io型のアップロードフロー（アップロード許可の発行 → ブラウザから画像ストレージへ直接送信 → 完了報告 → 署名付きURLでの配信）の全体シーケンス。

## 全体フロー

```mermaid
sequenceDiagram
    actor User as ブラウザ
    participant Server as アップロードサーバー
    participant Storage as 画像ストレージ(S3)
    participant CDN as 配信ネットワーク(CloudFront)

    Note over User,Server: 1. アップロード許可の発行
    User->>Server: アップロードしたい（利用者ID・画像の種類・サイズを申告）
    Server->>Server: 保存先を決定
    Server->>Storage: この保存先・条件でのアップロードを許可してほしい
    Storage-->>Server: 一時的なアップロード許可
    Server->>Server: アップロード記録を作成し、完了報告用のトークンを発行
    Server-->>User: アップロード許可・完了報告用トークンを返却

    Note over User,Storage: 2. 画像ストレージへの直接送信
    User->>Storage: 発行された許可とともに画像を送信<br/>(サーバーを経由しない)
    Storage-->>User: 送信完了

    Note over User,Server: 3. 完了報告
    User->>Server: アップロードが完了したと報告（完了報告用トークン付き）
    Server->>Server: トークンが正しいか確認
    Server->>Server: アップロード記録を確認
    Server->>Storage: 本当にアップロードされているか確認
    Storage-->>Server: 実体を確認できた
    Server->>Server: アップロード記録を「確定」にする<br/>(既に確定済みなら何もしない)
    Server-->>User: 完了報告を受理

    Note over User,CDN: 4. 確定済み画像の表示
    User->>Server: この画像を見たい
    Server->>Server: アップロード記録が「確定」済みか確認
    Server->>CDN: この画像を期限付きで見られるようにしてほしい
    CDN-->>Server: 期限付きの閲覧許可(署名付きURL)
    Server-->>User: 閲覧許可のURLへ転送
    User->>CDN: 閲覧許可のURLで画像を取得(自動で転送先へ)
    CDN-->>User: 画像データ
```

## 異常系

### アップロード許可の発行時のエラー

```mermaid
sequenceDiagram
    actor User as ブラウザ
    participant Server as アップロードサーバー
    participant Storage as 画像ストレージ(S3)

    User->>Server: アップロードしたい
    alt 利用者の認証情報が不正
        Server-->>User: 拒否（未認証）
    else 申告されたサイズが不正
        Server-->>User: 拒否（不正なリクエスト）
    else 申告された画像の種類が対象外
        Server-->>User: 拒否（不正なリクエスト）
    else 保存先が偶然重複した
        Server-->>User: 拒否（競合、リトライで解決）
    else 画像ストレージ側で許可が発行できなかった
        Server->>Storage: この保存先・条件でのアップロードを許可してほしい
        Storage-->>Server: 発行エラー
        Server-->>User: 拒否（サーバー内部エラー）
    end
```

### 完了報告時のエラー

```mermaid
sequenceDiagram
    actor User as ブラウザ
    participant Server as アップロードサーバー
    participant Storage as 画像ストレージ(S3)

    User->>Server: アップロードが完了したと報告
    alt 利用者の認証情報が不正
        Server-->>User: 拒否（未認証）
    else 完了報告用トークンが不正・期限切れ
        Server->>Server: トークンが正しいか確認
        Server-->>User: 拒否（未認証）
        Note right of Server: アップロード記録の有無を確認する前にトークンを<br/>確認するため、存在しない対象でも同じ応答になる
    else アップロード記録が存在しない
        Server->>Server: アップロード記録を確認
        Server-->>User: 拒否（対象が見つからない）
    else 画像ストレージに実体が見当たらない（送信の未完了・失敗）
        Server->>Storage: 本当にアップロードされているか確認
        Storage-->>Server: 見当たらない
        Server-->>User: 拒否（対象が見つからない）
        Note right of Server: アップロード記録は「確定」にならず、<br/>未確定のまま維持される
    end
```

### 画像表示時のエラー

```mermaid
sequenceDiagram
    actor User as ブラウザ
    participant Server as アップロードサーバー
    participant CDN as 配信ネットワーク(CloudFront)

    User->>Server: この画像を見たい
    alt アップロード記録が存在しない
        Server-->>User: 拒否（対象が見つからない）
    else まだ完了報告が済んでいない（未確定）
        Server-->>User: 拒否（対象が見つからない）
    else 配信ネットワーク側で閲覧許可が発行できなかった
        Server->>CDN: この画像を期限付きで見られるようにしてほしい
        CDN-->>Server: 発行エラー
        Server-->>User: 拒否（サーバー内部エラー）
    end
```

## 各ステップの要点

### 1. アップロード許可の発行

- 保存先（S3オブジェクトキー）は利用者IDと乱数から一意に生成し、アップロード記録の識別子と兼用する。利用者側は保存先を指定できない
- 発行される許可には「この画像サイズ・種類でしか使えない」という条件が完全一致で固定される（GitHub/esa.ioの実測分析に基づく設計。[S3アップロードAPIのレスポンス設計比較(GitHub/esa.io)](https://scrapbox.io/tkdn/S3アップロードAPIのレスポンス設計比較(GitHub/esa.io))参照）
- 完了報告用トークンは、対象の識別子と有効期限だけから検証できるステートレスな仕組みになっている

### 2. 画像ストレージへの直接送信

- ブラウザは画像ストレージへ直接送信し、アップロードサーバーはバイト列を一切中継しない
- この送信ではCORSの事前確認（プリフライト）が発生しない（[S3 POST PolicyアップロードでCORS preflightが発生しない理由](https://scrapbox.io/tkdn/S3_POST_PolicyアップロードでCORS_preflightが発生しない理由)参照）

### 3. 完了報告

- 完了報告用トークンの確認は、アップロード記録の有無を調べるより先に行う（不正なトークンから記録の存在有無を推測されないようにするため）
- 画像ストレージ側で実体を同期的に確認してから記録を「確定」にする。確認できなかった場合は拒否し、記録は未確定のまま
- 「確定」の処理は繰り返し呼んでも安全（最初に確定した時刻を保持し続ける）

### 4. 確定済み画像の表示

- ブラウザは画像を通常の`<img>`タグとして参照するだけで、期限付きの閲覧許可URLへの転送を自動的に追跡して表示する
- 完了報告がまだ済んでいない画像は「見つからない」扱いになる
- 検証目的のため認証・認可を行わず、保存先の推測困難性のみに依存している。実運用ではここでリクエスト元の認証とアクセス認可を必須にすること
