# S3アップロード + CloudFront Signed URL 発行サーバー Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** ブラウザから画像をS3へ直接アップロードし、アップロード完了後にCloudFront Signed URL(GET)を発行して閲覧できることを検証するための、`cmd/upload-server`とビルドレスのブラウザUIを実装する。

**Architecture:** 既存の署名専用CLI(`main.go`)を`cmd/sign-cli/`に移動し、新規に`cmd/upload-server/`というHTTPサーバーを追加する2バイナリ構成。サーバーは`POST /api/presign-upload`でS3 Presigned URL(PUT)を発行し、`POST /api/presign-download`で既存の`sign`パッケージを使いCloudFront Signed URL(GET)を発行する。画像バイト列はブラウザ→S3へ直接流れ、サーバーは中継しない。

**Tech Stack:** Go 1.27.0, `github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign` v1.12.1(既存), `github.com/aws/aws-sdk-go-v2/service/s3`(新規追加), `github.com/aws/aws-sdk-go-v2/config`(新規追加), 標準ライブラリ(`net/http`, `encoding/json`, `crypto/rand`, `mime`)

**Spec:** `docs/upload-plan.md`

## Global Constraints

- CLI引数のみで完結させる。環境変数は使わない(共有シークレットも`-upload-secret`フラグで受け取る)。
- CloudFront秘密鍵はPKCS8 PEMファイルをローカルパスで読む(`-private-key`フラグ)。サーバー起動時に一度だけ読み込み、`crypto.Signer`としてプロセス生存期間中保持する。
- S3側のAWS認証情報は独自フラグを作らず、`config.LoadDefaultConfig(ctx)`のデフォルト認証情報チェーンに任せる。
- アップロードファイルサイズ制限は設けない。
- S3オブジェクトキーはサーバー側で`users/{userId}/{ランダムID}{拡張子}`形式を生成し、クライアントが任意のキーを指定できないようにする。
- Content-Typeは`image/`プレフィックスのみ許可する。
- CORSは固定オリジン(`http://localhost:8080`)のみ許可し、`*`は使わない。
- 依存追加は最小限にする。UUID生成にサードパーティライブラリを追加せず、標準ライブラリ(`crypto/rand`)で代替する。

---

## File Structure

```
.
├── cmd/
│   ├── sign-cli/
│   │   └── main.go                 # 既存main.goを移動（内容は無変更）
│   └── upload-server/
│       ├── main.go                 # エントリーポイント、フラグパース、サーバー起動
│       ├── handler.go              # HTTPハンドラ本体（テスト対象）
│       ├── handler_test.go         # ハンドラの単体テスト
│       └── keygen.go               # S3オブジェクトキー生成ロジック（テスト対象）
│       └── keygen_test.go
├── web/
│   └── index.html                  # ビルドレスのブラウザUI
└── terraform/
    └── s3.tf                       # CORS設定を追加
```

`handler.go`は依存(`s3PresignAPI`・`cloudFrontSignAPI`)をインターフェースとして受け取り、実際のAWS SDK呼び出しをモックに差し替えてテストできる構成にする。`main.go`は実際のSDKクライアントを組み立ててハンドラに注入するだけの薄い層にする。

## Interfaces Overview

- `keygen.go`が`Produces`: `func newObjectKey(userID, contentType string) (key string, err error)`
- `handler.go`が`Consumes`: `newObjectKey`、および2つのインターフェース `s3Presigner` / `cloudFrontSigner`(下記Task 3で定義)
- `handler.go`が`Produces`: `func newUploadServer(cfg uploadServerConfig) *uploadServer`、`(*uploadServer) ServeMux() *http.ServeMux`
- `main.go`が`Consumes`: `newUploadServer`、`uploadServerConfig`

---

### Task 1: 既存main.goを`cmd/sign-cli/`に移動する

**Files:**
- Move: `main.go` → `cmd/sign-cli/main.go`

**Interfaces:**
- Consumes: なし
- Produces: `go run ./cmd/sign-cli`で既存CLIが動作すること

このタスクにテストコードはない。ビルド確認のみで完結する純粋な移動作業。

- [ ] **Step 1: ファイルを移動する**

```bash
mkdir -p cmd/sign-cli
git mv main.go cmd/sign-cli/main.go
```

- [ ] **Step 2: ビルドを確認する**

Run: `go build ./...`
Expected: エラーなく成功する(パッケージが`main`のままであれば`go.mod`のmoduleパス変更は不要)

- [ ] **Step 3: 既存の動作を確認する**

Run: `go run ./cmd/sign-cli -url "https://example.com/x" -key-pair-id "K" -private-key "keys/private_key.pem" -expires 1h`
Expected: 署名付きURLが標準出力に1行出力される(既存の`keys/private_key.pem`が存在する前提)

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
refactor: 署名CLIをcmd/sign-cliへ移動

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01K14V4GbcCYepGPaCozaxwt
EOF
)"
```

---

### Task 2: S3オブジェクトキー生成ロジック(`keygen.go`)

**Files:**
- Create: `cmd/upload-server/keygen.go`
- Test: `cmd/upload-server/keygen_test.go`

**Interfaces:**
- Consumes: なし(標準ライブラリの`crypto/rand`, `encoding/hex`, `mime`, `fmt`, `strings`のみ)
- Produces: `func newObjectKey(userID, contentType string) (string, error)` — Task 3で`handler.go`が使用する

`newObjectKey`は`userID`が空なら`errEmptyUserID`を返し、`contentType`が`image/`で始まらなければ`errInvalidContentType`を返す。成功時は`users/{userID}/{16バイトのhex文字列}{拡張子}`を返す。拡張子は`mime.ExtensionsByType(contentType)`の先頭要素を使う(候補がなければ拡張子なし)。

- [ ] **Step 1: 失敗ケースの失敗するテストを書く**

```go
// cmd/upload-server/keygen_test.go
package main

import (
	"errors"
	"strings"
	"testing"
)

func TestNewObjectKey_EmptyUserID(t *testing.T) {
	_, err := newObjectKey("", "image/png")
	if !errors.Is(err, errEmptyUserID) {
		t.Fatalf("got err=%v, want errEmptyUserID", err)
	}
}

func TestNewObjectKey_InvalidContentType(t *testing.T) {
	_, err := newObjectKey("alice", "application/octet-stream")
	if !errors.Is(err, errInvalidContentType) {
		t.Fatalf("got err=%v, want errInvalidContentType", err)
	}
}

func TestNewObjectKey_Success(t *testing.T) {
	key, err := newObjectKey("alice", "image/png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(key, "users/alice/") {
		t.Fatalf("key = %q, want prefix users/alice/", key)
	}
	if !strings.HasSuffix(key, ".png") {
		t.Fatalf("key = %q, want suffix .png", key)
	}
}

func TestNewObjectKey_Unique(t *testing.T) {
	key1, err := newObjectKey("alice", "image/png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	key2, err := newObjectKey("alice", "image/png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key1 == key2 {
		t.Fatalf("expected unique keys, got same key twice: %q", key1)
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./cmd/upload-server/... -run TestNewObjectKey -v`
Expected: FAIL (パッケージが存在しない、または`newObjectKey`/`errEmptyUserID`/`errInvalidContentType`が未定義のためコンパイルエラー)

- [ ] **Step 3: 実装を書く**

```go
// cmd/upload-server/keygen.go
package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"strings"
)

var (
	errEmptyUserID        = errors.New("userId must not be empty")
	errInvalidContentType = errors.New("contentType must start with image/")
)

func newObjectKey(userID, contentType string) (string, error) {
	if userID == "" {
		return "", errEmptyUserID
	}
	if !strings.HasPrefix(contentType, "image/") {
		return "", errInvalidContentType
	}

	ext := ""
	if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
		ext = exts[0]
	}

	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random id: %w", err)
	}

	return fmt.Sprintf("users/%s/%s%s", userID, hex.EncodeToString(buf), ext), nil
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./cmd/upload-server/... -run TestNewObjectKey -v`
Expected: PASS (4件すべて)

- [ ] **Step 5: Commit**

```bash
git add cmd/upload-server/keygen.go cmd/upload-server/keygen_test.go
git commit -m "$(cat <<'EOF'
feat: S3オブジェクトキー生成ロジックを追加

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01K14V4GbcCYepGPaCozaxwt
EOF
)"
```

---

### Task 3: HTTPハンドラ本体(`handler.go`) — アップロード発行エンドポイント

**Files:**
- Create: `cmd/upload-server/handler.go`
- Test: `cmd/upload-server/handler_test.go`

**Interfaces:**
- Consumes: `newObjectKey(userID, contentType string) (string, error)` (Task 2)
- Produces:
  - `type s3Presigner interface { PresignPutObject(ctx context.Context, bucket, key, contentType string) (url string, err error) }`
  - `type cloudFrontSigner interface { SignDownloadURL(key string) (url string, err error) }`
  - `type uploadServerConfig struct { UploadSecret string; S3Presigner s3Presigner; CloudFrontSigner cloudFrontSigner }`
  - `func newUploadServer(cfg uploadServerConfig) *uploadServer`
  - `func (s *uploadServer) ServeMux() *http.ServeMux`
  - Task 4はこの`ServeMux()`にダウンロードエンドポイントを追加する形で拡張する
  - Task 5(`main.go`)は`s3Presigner`・`cloudFrontSigner`の実装を用意して`uploadServerConfig`に注入する

`s3Presigner`・`cloudFrontSigner`をインターフェースにすることで、AWS実環境なしにハンドラの単体テストが書ける。

- [ ] **Step 1: 失敗するテストを書く(正常系・シークレット不一致・バリデーションエラー)**

```go
// cmd/upload-server/handler_test.go
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeS3Presigner struct {
	url string
	err error
}

func (f *fakeS3Presigner) PresignPutObject(ctx context.Context, bucket, key, contentType string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.url, nil
}

type fakeCloudFrontSigner struct {
	url string
	err error
}

func (f *fakeCloudFrontSigner) SignDownloadURL(key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.url, nil
}

func newTestServer() *uploadServer {
	return newUploadServer(uploadServerConfig{
		UploadSecret:     "test-secret",
		S3Presigner:      &fakeS3Presigner{url: "https://bucket.s3.example.com/signed-put"},
		CloudFrontSigner: &fakeCloudFrontSigner{url: "https://cdn.example.com/signed-get"},
	})
}

func TestPresignUpload_Success(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-upload", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Key       string `json:"key"`
		UploadURL string `json:"uploadUrl"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.UploadURL != "https://bucket.s3.example.com/signed-put" {
		t.Fatalf("uploadUrl = %q, unexpected", got.UploadURL)
	}
	if !strings.HasPrefix(got.Key, "users/alice/") {
		t.Fatalf("key = %q, want prefix users/alice/", got.Key)
	}
}

func TestPresignUpload_WrongSecret(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-upload", body)
	req.Header.Set("X-Upload-Secret", "wrong-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPresignUpload_MissingSecret(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-upload", body)
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPresignUpload_InvalidContentType(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"userId":"alice","contentType":"application/octet-stream"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-upload", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPresignUpload_EmptyUserID(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"userId":"","contentType":"image/png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-upload", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./cmd/upload-server/... -run TestPresignUpload -v`
Expected: FAIL (コンパイルエラー: `uploadServerConfig`/`newUploadServer`/`uploadServer`等が未定義)

- [ ] **Step 3: 実装を書く**

```go
// cmd/upload-server/handler.go
package main

import (
	"context"
	"encoding/json"
	"net/http"
)

type s3Presigner interface {
	PresignPutObject(ctx context.Context, bucket, key, contentType string) (url string, err error)
}

type cloudFrontSigner interface {
	SignDownloadURL(key string) (url string, err error)
}

type uploadServerConfig struct {
	Bucket           string
	UploadSecret     string
	S3Presigner      s3Presigner
	CloudFrontSigner cloudFrontSigner
}

type uploadServer struct {
	cfg uploadServerConfig
	mux *http.ServeMux
}

func newUploadServer(cfg uploadServerConfig) *uploadServer {
	s := &uploadServer{cfg: cfg, mux: http.NewServeMux()}
	s.mux.HandleFunc("/api/presign-upload", s.handlePresignUpload)
	return s
}

func (s *uploadServer) ServeMux() *http.ServeMux {
	return s.mux
}

func (s *uploadServer) checkSecret(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Upload-Secret") != s.cfg.UploadSecret {
		http.Error(w, "invalid or missing X-Upload-Secret header", http.StatusUnauthorized)
		return false
	}
	return true
}

type presignUploadRequest struct {
	UserID      string `json:"userId"`
	ContentType string `json:"contentType"`
}

type presignUploadResponse struct {
	Key       string `json:"key"`
	UploadURL string `json:"uploadUrl"`
}

func (s *uploadServer) handlePresignUpload(w http.ResponseWriter, r *http.Request) {
	if !s.checkSecret(w, r) {
		return
	}

	var req presignUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	key, err := newObjectKey(req.UserID, req.ContentType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	uploadURL, err := s.cfg.S3Presigner.PresignPutObject(r.Context(), s.cfg.Bucket, key, req.ContentType)
	if err != nil {
		http.Error(w, "presign upload url: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(presignUploadResponse{Key: key, UploadURL: uploadURL})
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./cmd/upload-server/... -run TestPresignUpload -v`
Expected: PASS (5件すべて)

- [ ] **Step 5: Commit**

```bash
git add cmd/upload-server/handler.go cmd/upload-server/handler_test.go
git commit -m "$(cat <<'EOF'
feat: Presigned URL(PUT)発行エンドポイントを追加

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01K14V4GbcCYepGPaCozaxwt
EOF
)"
```

---

### Task 4: ダウンロード用エンドポイント(`/api/presign-download`)

**Files:**
- Modify: `cmd/upload-server/handler.go`
- Modify: `cmd/upload-server/handler_test.go`

**Interfaces:**
- Consumes: `cloudFrontSigner`(Task 3で定義済み)
- Produces: `handlePresignDownload`が`s.mux`に`/api/presign-download`として登録される。Task 5の`main.go`はここで完成した`ServeMux()`をそのまま`http.ListenAndServe`に渡す。

- [ ] **Step 1: 失敗するテストを書く**

```go
// cmd/upload-server/handler_test.go に追記

func TestPresignDownload_Success(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"key":"users/alice/abc123.png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-download", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		DownloadURL string `json:"downloadUrl"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.DownloadURL != "https://cdn.example.com/signed-get" {
		t.Fatalf("downloadUrl = %q, unexpected", got.DownloadURL)
	}
}

func TestPresignDownload_WrongSecret(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"key":"users/alice/abc123.png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-download", body)
	req.Header.Set("X-Upload-Secret", "wrong-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPresignDownload_EmptyKey(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"key":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-download", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./cmd/upload-server/... -run TestPresignDownload -v`
Expected: FAIL (`/api/presign-download`が未登録のため404が返り、アサーションが失敗する)

- [ ] **Step 3: 実装を書く**

```go
// cmd/upload-server/handler.go の newUploadServer 内に追記
func newUploadServer(cfg uploadServerConfig) *uploadServer {
	s := &uploadServer{cfg: cfg, mux: http.NewServeMux()}
	s.mux.HandleFunc("/api/presign-upload", s.handlePresignUpload)
	s.mux.HandleFunc("/api/presign-download", s.handlePresignDownload)
	return s
}
```

```go
// cmd/upload-server/handler.go の末尾に追記
type presignDownloadRequest struct {
	Key string `json:"key"`
}

type presignDownloadResponse struct {
	DownloadURL string `json:"downloadUrl"`
}

func (s *uploadServer) handlePresignDownload(w http.ResponseWriter, r *http.Request) {
	if !s.checkSecret(w, r) {
		return
	}

	var req presignDownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Key == "" {
		http.Error(w, "key must not be empty", http.StatusBadRequest)
		return
	}

	downloadURL, err := s.cfg.CloudFrontSigner.SignDownloadURL(req.Key)
	if err != nil {
		http.Error(w, "sign download url: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(presignDownloadResponse{DownloadURL: downloadURL})
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./cmd/upload-server/... -v`
Expected: PASS (Task 2〜4のテストすべて、8件以上)

- [ ] **Step 5: Commit**

```bash
git add cmd/upload-server/handler.go cmd/upload-server/handler_test.go
git commit -m "$(cat <<'EOF'
feat: CloudFront Signed URL(GET)発行エンドポイントを追加

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01K14V4GbcCYepGPaCozaxwt
EOF
)"
```

---

### Task 5: 実SDKアダプターと`main.go`(サーバー起動)

**Files:**
- Create: `cmd/upload-server/main.go`
- Create: `cmd/upload-server/s3_adapter.go`
- Create: `cmd/upload-server/cloudfront_adapter.go`

**Interfaces:**
- Consumes: `s3Presigner`・`cloudFrontSigner`インターフェース(Task 3)、`newUploadServer`(Task 3)、`sign.LoadPEMPrivKeyPKCS8AsSigner`・`sign.NewURLSigner`・`sign.Policy`・`sign.Statement`・`sign.Condition`・`sign.NewAWSEpochTime`(既存の`sign`パッケージ、`cmd/sign-cli/main.go`と同じ使い方)
- Produces: `go run ./cmd/upload-server`で実際にサーバーが起動する

このタスクはAWS実環境への接続を含むため、自動テストは書かない(ユニットテストで検証済みのハンドラに実アダプターを注入するだけの配線コード)。ビルド確認と、後続タスクでの手動検証に委ねる。

- [ ] **Step 1: S3アダプターを実装する**

```go
// cmd/upload-server/s3_adapter.go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type realS3Presigner struct {
	client  *s3.PresignClient
	expires time.Duration
}

func newRealS3Presigner(client *s3.Client, expires time.Duration) *realS3Presigner {
	return &realS3Presigner{client: s3.NewPresignClient(client), expires: expires}
}

func (p *realS3Presigner) PresignPutObject(ctx context.Context, bucket, key, contentType string) (string, error) {
	req, err := p.client.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}, s3.WithPresignExpires(p.expires))
	if err != nil {
		return "", fmt.Errorf("presign put object: %w", err)
	}
	return req.URL, nil
}
```

- [ ] **Step 2: CloudFrontアダプターを実装する**

```go
// cmd/upload-server/cloudfront_adapter.go
package main

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
)

type realCloudFrontSigner struct {
	domain    string
	urlSigner *sign.URLSigner
	expires   time.Duration
}

func newRealCloudFrontSigner(domain, keyPairID string, signer *sign.URLSigner, expires time.Duration) *realCloudFrontSigner {
	return &realCloudFrontSigner{domain: domain, urlSigner: signer, expires: expires}
}

func (c *realCloudFrontSigner) SignDownloadURL(key string) (string, error) {
	url := fmt.Sprintf("https://%s/%s", c.domain, key)
	policy := &sign.Policy{
		Statements: []sign.Statement{
			{
				Resource: url,
				Condition: sign.Condition{
					DateLessThan: sign.NewAWSEpochTime(time.Now().Add(c.expires)),
				},
			},
		},
	}
	signedURL, err := c.urlSigner.SignWithPolicy(url, policy)
	if err != nil {
		return "", fmt.Errorf("sign with policy: %w", err)
	}
	return signedURL, nil
}
```

- [ ] **Step 3: main.goを実装する**

```go
// cmd/upload-server/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", ":8080", "HTTP server listen address")
	bucket := flag.String("bucket", "", "S3 bucket name for uploads (required)")
	cloudfrontDomain := flag.String("cloudfront-domain", "", "CloudFront domain for signed download URLs (required)")
	keyPairID := flag.String("key-pair-id", "", "CloudFront public key ID (required)")
	privateKeyPath := flag.String("private-key", "", "path to PKCS8 PEM private key (required)")
	uploadSecret := flag.String("upload-secret", "", "shared secret required in X-Upload-Secret header (required)")
	expires := flag.Duration("expires", 15*time.Minute, "signed download URL validity duration from now")
	flag.Parse()

	if *bucket == "" || *cloudfrontDomain == "" || *keyPairID == "" || *privateKeyPath == "" || *uploadSecret == "" {
		flag.Usage()
		return fmt.Errorf("missing required flag(s)")
	}

	f, err := os.Open(*privateKeyPath)
	if err != nil {
		return fmt.Errorf("open private key: %w", err)
	}
	defer f.Close()

	signer, err := sign.LoadPEMPrivKeyPKCS8AsSigner(f)
	if err != nil {
		return fmt.Errorf("load private key: %w", err)
	}

	ctx := context.Background()
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}
	s3Client := s3.NewFromConfig(awsCfg)

	urlSigner := sign.NewURLSigner(*keyPairID, signer)

	srv := newUploadServer(uploadServerConfig{
		Bucket:           *bucket,
		UploadSecret:     *uploadSecret,
		S3Presigner:      newRealS3Presigner(s3Client, *expires),
		CloudFrontSigner: newRealCloudFrontSigner(*cloudfrontDomain, *keyPairID, urlSigner, *expires),
	})
	srv.ServeMux().Handle("/", http.FileServer(http.Dir("web")))

	fmt.Fprintf(os.Stderr, "listening on %s\n", *addr)
	return http.ListenAndServe(*addr, srv.ServeMux())
}
```

- [ ] **Step 4: 依存を取得しビルドを確認する**

Run:
```bash
go get github.com/aws/aws-sdk-go-v2/service/s3
go get github.com/aws/aws-sdk-go-v2/config
go build ./...
go vet ./...
```
Expected: すべて成功する

- [ ] **Step 5: ユニットテストが依然として通ることを確認する**

Run: `go test ./... -v`
Expected: PASS (Task 2〜4のテストすべて。Task 5自体はテストを追加しないため件数は変わらない)

- [ ] **Step 6: Commit**

```bash
git add cmd/upload-server/main.go cmd/upload-server/s3_adapter.go cmd/upload-server/cloudfront_adapter.go go.mod go.sum
git commit -m "$(cat <<'EOF'
feat: upload-serverの実SDKアダプターと起動処理を実装

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01K14V4GbcCYepGPaCozaxwt
EOF
)"
```

---

### Task 6: ブラウザUI(`web/index.html`)

**Files:**
- Create: `web/index.html`

**Interfaces:**
- Consumes: `POST /api/presign-upload`(Task 3)、`POST /api/presign-download`(Task 4)のJSONスキーマ
- Produces: なし(末端のUI)

自動テストなし。Task 7の手動検証で動作確認する。

- [ ] **Step 1: HTMLを実装する**

```html
<!-- web/index.html -->
<!DOCTYPE html>
<html lang="ja">
<head>
<meta charset="utf-8">
<title>Upload &amp; Signed URL Demo</title>
</head>
<body>
<h1>S3 Upload -&gt; CloudFront Signed URL Demo</h1>

<label>User ID: <input id="userId" value="alice"></label><br>
<label>Upload Secret: <input id="secret" value="DONT_USE_THIS_CODE"></label><br>
<input type="file" id="file" accept="image/*"><br>
<button id="uploadBtn">Upload</button>

<pre id="log"></pre>
<a id="result" href="#" target="_blank" style="display:none">Open signed URL</a>

<script>
function log(msg) {
  document.getElementById('log').textContent += msg + "\n";
}

document.getElementById('uploadBtn').addEventListener('click', async () => {
  const userId = document.getElementById('userId').value;
  const secret = document.getElementById('secret').value;
  const fileInput = document.getElementById('file');
  const file = fileInput.files[0];
  if (!file) {
    log('ファイルを選択してください');
    return;
  }

  log('presign-upload を要求中...');
  const presignRes = await fetch('/api/presign-upload', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Upload-Secret': secret,
    },
    body: JSON.stringify({ userId: userId, contentType: file.type }),
  });
  if (!presignRes.ok) {
    log('presign-upload 失敗: ' + presignRes.status);
    return;
  }
  const { key, uploadUrl } = await presignRes.json();
  log('key = ' + key);

  log('S3へPUT中...');
  const putRes = await fetch(uploadUrl, {
    method: 'PUT',
    headers: { 'Content-Type': file.type },
    body: file,
  });
  if (!putRes.ok) {
    log('S3 PUT 失敗: ' + putRes.status);
    return;
  }
  log('S3 PUT 成功 (status=' + putRes.status + ')');

  log('presign-download を要求中...');
  const downloadRes = await fetch('/api/presign-download', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Upload-Secret': secret,
    },
    body: JSON.stringify({ key: key }),
  });
  if (!downloadRes.ok) {
    log('presign-download 失敗: ' + downloadRes.status);
    return;
  }
  const { downloadUrl } = await downloadRes.json();
  log('downloadUrl 取得完了');

  const a = document.getElementById('result');
  a.href = downloadUrl;
  a.style.display = 'inline';
  a.textContent = downloadUrl;
});
</script>
</body>
</html>
```

- [ ] **Step 2: Commit**

```bash
git add web/index.html
git commit -m "$(cat <<'EOF'
feat: アップロード検証用のビルドレスブラウザUIを追加

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01K14V4GbcCYepGPaCozaxwt
EOF
)"
```

---

### Task 7: Terraform CORS設定の追加

**Files:**
- Modify: `terraform/s3.tf`

**Interfaces:**
- Consumes: なし
- Produces: S3バケットが`http://localhost:8080`からのPUTをCORSで許可する

- [ ] **Step 1: 現在のs3.tfを確認する**

Run: `cat terraform/s3.tf`
Expected: 既存の`aws_s3_bucket`・`aws_s3_bucket_public_access_block`・`aws_s3_bucket_policy`のみが定義されている

- [ ] **Step 2: CORS設定を追記する**

`terraform/s3.tf`の末尾に追記:

```hcl
resource "aws_s3_bucket_cors_configuration" "this" {
  bucket = aws_s3_bucket.this.id

  cors_rule {
    allowed_origins = ["http://localhost:8080"]
    allowed_methods = ["PUT"]
    allowed_headers = ["Content-Type"]
    max_age_seconds = 3000
  }
}
```

`aws_s3_bucket.this`のリソース名は既存の`s3.tf`内の実際のリソース名に合わせて調整すること(既存コードを`Read`してから正確な名前を確認する)。

- [ ] **Step 3: plan/applyを確認する**

Run:
```bash
cd terraform
terraform plan
```
Expected: `aws_s3_bucket_cors_configuration.this`の新規作成のみが差分として表示される(既存リソースへの変更・削除がないこと)

ユーザーの承認を得てから`terraform apply`を実行する(このステップはAWS実環境への変更を伴うため、実行前に必ずユーザーに確認する)。

- [ ] **Step 4: Commit**

```bash
git add terraform/s3.tf
git commit -m "$(cat <<'EOF'
feat: S3バケットにアップロード用CORS設定を追加

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01K14V4GbcCYepGPaCozaxwt
EOF
)"
```

---

### Task 8: 手動検証(E2E)

**Files:**
- なし(検証のみ)

**Interfaces:**
- Consumes: Task 1〜7で実装したすべて

自動化しない手動確認。`docs/upload-plan.md`の「検証方法」節に基づく。

- [ ] **Step 1: サーバーを起動する**

```bash
go run ./cmd/upload-server \
  -bucket "$(terraform -chdir=terraform output -raw s3_bucket_name)" \
  -cloudfront-domain "$(terraform -chdir=terraform output -raw cloudfront_domain_name)" \
  -key-pair-id "$(terraform -chdir=terraform output -raw cloudfront_public_key_id)" \
  -private-key "keys/private_key.pem" \
  -upload-secret "DONT_USE_THIS_CODE"
```

- [ ] **Step 2: ブラウザで`http://localhost:8080/`を開き、画像をアップロードする**

Expected: ログに「S3 PUT 成功」「downloadUrl 取得完了」が表示され、リンクをクリックすると画像が表示される(200)

- [ ] **Step 3: 開発者ツールのNetworkタブで、PUTリクエストの宛先を確認する**

Expected: リクエストURLのホストが`<bucket>.s3.<region>.amazonaws.com`であり、`http://localhost:8080`を経由していないこと

- [ ] **Step 4: シークレットなしでの拒否を確認する**

```bash
curl -i -X POST http://localhost:8080/api/presign-upload \
  -H "Content-Type: application/json" \
  -d '{"userId":"alice","contentType":"image/png"}'
```
Expected: `401 Unauthorized`

- [ ] **Step 5: 不正なContent-Typeでの拒否を確認する**

```bash
curl -i -X POST http://localhost:8080/api/presign-upload \
  -H "Content-Type: application/json" \
  -H "X-Upload-Secret: DONT_USE_THIS_CODE" \
  -d '{"userId":"alice","contentType":"application/octet-stream"}'
```
Expected: `400 Bad Request`

- [ ] **Step 6: 固定オリジン以外からのPUTがCORSでブロックされることを確認する**

Task 2で取得した`uploadUrl`をブラウザの開発者ツールのConsoleから、`http://localhost:8080`以外のオリジン(例: 別タブで`https://example.com`を開きそのConsoleから)`fetch(uploadUrl, {method: 'PUT', body: new Blob(['x'])})`を実行する。

Expected: CORSエラー(`Access-Control-Allow-Origin`ヘッダーが一致しない旨のエラー)がConsoleに表示され、リクエストが失敗する

- [ ] **Step 7: この計画の完了をユーザーに報告する**

すべてのステップが期待通りであれば、`docs/upload-plan.md`の検証項目(a)〜(e)がすべて満たされたことになる。
