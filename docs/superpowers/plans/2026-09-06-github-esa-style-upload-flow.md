# GitHub/esa.io型アップロードフロー刷新 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `cmd/upload-server`を、Presigned PUT方式からGitHub/esa.io実測分析に基づくS3 POST Policy方式へ全面刷新し、「レコード作成→S3 POST→完了通知→確定」というライフサイクルを持つアップロードフローを実現する。

**Architecture:** クライアントはまず`POST /api/upload/policies`でS3 POST Policy一式とレコードIDを取得し、そのpolicyでS3へ直接POSTする。アップロード完了後`PATCH /api/upload/assets/{id}`で完了通知し、サーバーは`HeadObject`で実体を確認してからレコードを確定させる。ダウンロードは`GET /assets/{id}`でCloudFront Signed URLへ302リダイレクトする。レコードストアはインターフェースで抽象化したインメモリ実装。完了通知の認可はステートレスなHMACトークン（`confirmToken`）で行う。

**Tech Stack:** Go 1.27.1、`aws-sdk-go-v2/service/s3`（`PresignClient.PresignPostObject`、`Client.HeadObject`）、`aws-sdk-go-v2/feature/cloudfront/sign`（既存流用）、標準ライブラリ`net/http`（`http.ServeMux`のmethod-awareパターン）、`crypto/hmac`、`crypto/sha256`、`crypto/subtle`。

**Spec:** このplanは会話履歴中のgrillingセッション（2026-09-06、本ファイルの直前の会話ターン群）で確定した設計をspecとして扱う。既存の関連ドキュメントは`docs/upload-plan.md`（旧Presigned PUT版の設計、本plan適用後は一部内容が古くなる）。

## Global Constraints

- Go 1.27.1、`http.ServeMux`のmethod-awareパターン（`"POST /path"`, `"PATCH /path"`, `"GET /path"`）を使う
- 全てのインターフェース実装に`var _ Interface = (*Type)(nil)`のコンパイル時アサーションを付ける（既存パターン踏襲）
- 認証比較は全て`crypto/subtle.ConstantTimeCompare`を使う（既存の`checkSecret`踏襲）
- 検証目的のリポジトリであり自動E2Eテストは実装しない。ユニットテストのみTDDで書く
- 各タスク完了時に`go test ./...`と`golangci-lint run`を通す
- コメントは「なぜ」が非自明な箇所にのみ1行で書く。「何をしているか」の説明は書かない
- `id`はS3オブジェクトキーと兼用し、別途のID生成は行わない（既存の`newObjectKey`をそのまま使う）
- レコードストアはインターフェースで抽象化し、インメモリ実装の他にRDB等へ差し替え可能な設計にする
- HMACトークンの秘密鍵はCLIフラグにせず、コード内の`const`またはパッケージ変数として保持する

---

### Task 1: 確認トークン（confirmToken）の生成・検証

**Files:**
- Create: `cmd/upload-server/token.go`
- Test: `cmd/upload-server/token_test.go`

**Interfaces:**
- Consumes: なし（標準ライブラリのみ）
- Produces:
  - `newConfirmToken(id string, expiresAt time.Time) string` — `id`と`expiresAt`(RFC3339)からHMAC-SHA256トークンを生成し `"<base64url(mac)>.<unixExpiresAt>"` 形式の文字列を返す
  - `verifyConfirmToken(id, token string) bool` — トークンをパースし、有効期限切れなら`false`、MACが一致しなければ`false`、一致すれば`true`を返す
  - `confirmTokenSecret` — パッケージレベルの`[]byte`定数相当（`var confirmTokenSecret = []byte("...")`。Goの`const`は`[]byte`を許さないため`var`にする）

- [ ] **Step 1: Write the failing test for token generation and verification round-trip**

```go
// cmd/upload-server/token_test.go
package main

import (
	"strings"
	"testing"
	"time"
)

func TestConfirmToken_RoundTrip(t *testing.T) {
	id := "users/alice/abc123.png"
	token := newConfirmToken(id, time.Now().Add(15*time.Minute))

	if !verifyConfirmToken(id, token) {
		t.Fatalf("verifyConfirmToken(%q, %q) = false, want true", id, token)
	}
}

func TestConfirmToken_WrongID(t *testing.T) {
	token := newConfirmToken("users/alice/abc123.png", time.Now().Add(15*time.Minute))

	if verifyConfirmToken("users/bob/abc123.png", token) {
		t.Fatalf("verifyConfirmToken with wrong id = true, want false")
	}
}

func TestConfirmToken_Expired(t *testing.T) {
	id := "users/alice/abc123.png"
	token := newConfirmToken(id, time.Now().Add(-1*time.Minute))

	if verifyConfirmToken(id, token) {
		t.Fatalf("verifyConfirmToken with expired token = true, want false")
	}
}

func TestConfirmToken_TamperedSignature(t *testing.T) {
	id := "users/alice/abc123.png"
	token := newConfirmToken(id, time.Now().Add(15*time.Minute))
	parts := strings.SplitN(token, ".", 2)
	tampered := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA." + parts[1]

	if verifyConfirmToken(id, tampered) {
		t.Fatalf("verifyConfirmToken with tampered signature = true, want false")
	}
}

func TestConfirmToken_MalformedToken(t *testing.T) {
	if verifyConfirmToken("users/alice/abc123.png", "not-a-valid-token") {
		t.Fatalf("verifyConfirmToken with malformed token = true, want false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/upload-server/... -run TestConfirmToken -v`
Expected: FAIL with `undefined: newConfirmToken` (compile error)

- [ ] **Step 3: Write minimal implementation**

```go
// cmd/upload-server/token.go
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// confirmTokenSecret is process-local by design: this repository is a
// verification sandbox, not a multi-instance deployment, so there is no
// need to share it via flags, env vars, or Secrets Manager.
var confirmTokenSecret = []byte("DONT_USE_THIS_CODE_confirm_token_secret")

func newConfirmToken(id string, expiresAt time.Time) string {
	exp := expiresAt.Unix()
	mac := computeConfirmTokenMAC(id, exp)
	return fmt.Sprintf("%s.%d", base64.RawURLEncoding.EncodeToString(mac), exp)
}

func verifyConfirmToken(id, token string) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return false
	}
	if time.Now().After(time.Unix(exp, 0)) {
		return false
	}
	want := computeConfirmTokenMAC(id, exp)
	return subtle.ConstantTimeCompare(sig, want) == 1
}

func computeConfirmTokenMAC(id string, exp int64) []byte {
	mac := hmac.New(sha256.New, confirmTokenSecret)
	mac.Write([]byte(id))
	mac.Write([]byte("."))
	mac.Write([]byte(strconv.FormatInt(exp, 10)))
	return mac.Sum(nil)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/upload-server/... -run TestConfirmToken -v`
Expected: PASS (all 5 subtests)

- [ ] **Step 5: Run golangci-lint**

Run: `golangci-lint run ./cmd/upload-server/...`
Expected: no findings

- [ ] **Step 6: Commit**

```bash
git add cmd/upload-server/token.go cmd/upload-server/token_test.go
git commit -m "feat(upload-server): add stateless HMAC confirm token"
```

---

### Task 2: アセットレコードストア（interface + インメモリ実装）

**Files:**
- Create: `cmd/upload-server/store.go`
- Test: `cmd/upload-server/store_test.go`

**Interfaces:**
- Consumes: なし
- Produces:
  - `type assetRecord struct { ID, UserID, ContentType string; Size int64; CreatedAt time.Time; ConfirmedAt *time.Time }`
  - `type assetStore interface { Create(ctx context.Context, rec assetRecord) error; Get(ctx context.Context, id string) (assetRecord, error); Confirm(ctx context.Context, id string, confirmedAt time.Time) error }`
  - `var errAssetNotFound = errors.New("asset record not found")` — `Get`/`Confirm`が対象レコードを見つけられない時に返す（`errors.Is`で判定可能）
  - `type memoryAssetStore struct { ... }`、`func newMemoryAssetStore() *memoryAssetStore`
  - `var _ assetStore = (*memoryAssetStore)(nil)`

- [ ] **Step 1: Write the failing test**

```go
// cmd/upload-server/store_test.go
package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryAssetStore_CreateAndGet(t *testing.T) {
	store := newMemoryAssetStore()
	ctx := context.Background()
	rec := assetRecord{
		ID:          "users/alice/abc123.png",
		UserID:      "alice",
		ContentType: "image/png",
		Size:        22945,
		CreatedAt:   time.Now(),
	}

	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: unexpected error: %v", err)
	}

	got, err := store.Get(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if got.ID != rec.ID || got.UserID != rec.UserID || got.Size != rec.Size {
		t.Fatalf("Get = %+v, want %+v", got, rec)
	}
	if got.ConfirmedAt != nil {
		t.Fatalf("ConfirmedAt = %v, want nil", got.ConfirmedAt)
	}
}

func TestMemoryAssetStore_GetNotFound(t *testing.T) {
	store := newMemoryAssetStore()
	_, err := store.Get(context.Background(), "users/alice/missing.png")
	if !errors.Is(err, errAssetNotFound) {
		t.Fatalf("Get: err = %v, want errAssetNotFound", err)
	}
}

func TestMemoryAssetStore_Confirm(t *testing.T) {
	store := newMemoryAssetStore()
	ctx := context.Background()
	rec := assetRecord{ID: "users/alice/abc123.png", UserID: "alice", ContentType: "image/png", Size: 100, CreatedAt: time.Now()}
	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: unexpected error: %v", err)
	}

	confirmedAt := time.Now()
	if err := store.Confirm(ctx, rec.ID, confirmedAt); err != nil {
		t.Fatalf("Confirm: unexpected error: %v", err)
	}

	got, err := store.Get(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if got.ConfirmedAt == nil {
		t.Fatalf("ConfirmedAt = nil, want non-nil")
	}
	if !got.ConfirmedAt.Equal(confirmedAt) {
		t.Fatalf("ConfirmedAt = %v, want %v", *got.ConfirmedAt, confirmedAt)
	}
}

func TestMemoryAssetStore_ConfirmNotFound(t *testing.T) {
	store := newMemoryAssetStore()
	err := store.Confirm(context.Background(), "users/alice/missing.png", time.Now())
	if !errors.Is(err, errAssetNotFound) {
		t.Fatalf("Confirm: err = %v, want errAssetNotFound", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/upload-server/... -run TestMemoryAssetStore -v`
Expected: FAIL with `undefined: newMemoryAssetStore` (compile error)

- [ ] **Step 3: Write minimal implementation**

```go
// cmd/upload-server/store.go
package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

var errAssetNotFound = errors.New("asset record not found")

type assetRecord struct {
	ID          string
	UserID      string
	ContentType string
	Size        int64
	CreatedAt   time.Time
	ConfirmedAt *time.Time
}

type assetStore interface {
	Create(ctx context.Context, rec assetRecord) error
	Get(ctx context.Context, id string) (assetRecord, error)
	Confirm(ctx context.Context, id string, confirmedAt time.Time) error
}

type memoryAssetStore struct {
	mu      sync.Mutex
	records map[string]assetRecord
}

var _ assetStore = (*memoryAssetStore)(nil)

func newMemoryAssetStore() *memoryAssetStore {
	return &memoryAssetStore{records: make(map[string]assetRecord)}
}

func (s *memoryAssetStore) Create(_ context.Context, rec assetRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[rec.ID] = rec
	return nil
}

func (s *memoryAssetStore) Get(_ context.Context, id string) (assetRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return assetRecord{}, errAssetNotFound
	}
	return rec, nil
}

func (s *memoryAssetStore) Confirm(_ context.Context, id string, confirmedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	if !ok {
		return errAssetNotFound
	}
	rec.ConfirmedAt = &confirmedAt
	s.records[id] = rec
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/upload-server/... -run TestMemoryAssetStore -v`
Expected: PASS (all 4 subtests)

- [ ] **Step 5: Run golangci-lint**

Run: `golangci-lint run ./cmd/upload-server/...`
Expected: no findings

- [ ] **Step 6: Commit**

```bash
git add cmd/upload-server/store.go cmd/upload-server/store_test.go
git commit -m "feat(upload-server): add asset record store interface and in-memory impl"
```

---

### Task 3: S3アダプタをPOST Policy + HeadObjectへ刷新

**Files:**
- Modify: `cmd/upload-server/s3_adapter.go`（全面書き換え）
- Test: `cmd/upload-server/s3_adapter_test.go` (新規。既存のhandler_test.goに混在していたfakeをここに集約せず、ハンドラのfakeはTask 4で別途定義するため、このテストはreal実装の最小限のコンパイル確認のみ)

**Interfaces:**
- Consumes: `github.com/aws/aws-sdk-go-v2/service/s3`の`s3.Client`、`s3.PresignClient`、`s3.PresignPostObject`、`s3.HeadObject`
- Produces:
  - `type postPolicyForm struct { URL string; Fields map[string]string }`
  - `type s3PostPolicyPresigner interface { PresignPostPolicy(ctx context.Context, bucket, key, contentType string, size int64, expires time.Duration) (postPolicyForm, error) }`
  - `type s3ObjectHeadChecker interface { HeadObject(ctx context.Context, bucket, key string) error }`
  - `type realS3Adapter struct { ... }`、`func newRealS3Adapter(client *s3.Client) *realS3Adapter`
  - `var _ s3PostPolicyPresigner = (*realS3Adapter)(nil)`
  - `var _ s3ObjectHeadChecker = (*realS3Adapter)(nil)`

**Note:** 既存の`s3Presigner`インターフェース（`PresignPutObject`）とその実装は本タスクで削除する。この時点で`handler.go`・`handler_test.go`・`main.go`はコンパイルが壊れるが、Task 4・5で追随するので一時的に許容する（このタスク単体でのビルド確認は`go build ./cmd/upload-server/...`ではなく`go vet cmd/upload-server/s3_adapter.go cmd/upload-server/s3_adapter_test.go`等ファイル単位で行うか、後続タスクまで`go test ./...`は保留する）。

- [ ] **Step 1: Write the failing test for the real adapter's interface satisfaction and form field shape**

```go
// cmd/upload-server/s3_adapter_test.go
package main

import (
	"testing"
)

func TestPostPolicyForm_FieldsShape(t *testing.T) {
	form := postPolicyForm{
		URL: "https://bucket.s3.amazonaws.com/",
		Fields: map[string]string{
			"key":    "users/alice/abc123.png",
			"policy": "eyJ...",
		},
	}
	if form.URL == "" {
		t.Fatalf("URL must not be empty")
	}
	if form.Fields["key"] != "users/alice/abc123.png" {
		t.Fatalf("Fields[key] = %q, unexpected", form.Fields["key"])
	}
}
```

このテストは型の形状確認のみで、実際のAWS呼び出しはモックしない（検証目的のリポジトリ方針に合わせ、`realS3Adapter`自体の単体テストはAWSエンドポイントへの依存を避けて割愛し、E2Eはブラウザでの手動検証に委ねる。既存の`realS3Presigner`/`realCloudFrontSigner`も同様にテスト対象外だった）。

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/upload-server/... -run TestPostPolicyForm -v`
Expected: FAIL with `undefined: postPolicyForm` (compile error)

- [ ] **Step 3: Write the implementation**

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

type postPolicyForm struct {
	URL    string            `json:"url"`
	Fields map[string]string `json:"fields"`
}

type s3PostPolicyPresigner interface {
	PresignPostPolicy(ctx context.Context, bucket, key, contentType string, size int64, expires time.Duration) (postPolicyForm, error)
}

type s3ObjectHeadChecker interface {
	HeadObject(ctx context.Context, bucket, key string) error
}

type realS3Adapter struct {
	client        *s3.Client
	presignClient *s3.PresignClient
}

var _ s3PostPolicyPresigner = (*realS3Adapter)(nil)
var _ s3ObjectHeadChecker = (*realS3Adapter)(nil)

func newRealS3Adapter(client *s3.Client) *realS3Adapter {
	return &realS3Adapter{client: client, presignClient: s3.NewPresignClient(client)}
}

func (a *realS3Adapter) PresignPostPolicy(ctx context.Context, bucket, key, contentType string, size int64, expires time.Duration) (postPolicyForm, error) {
	req, err := a.presignClient.PresignPostObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}, func(o *s3.PresignPostOptions) {
		o.Expires = expires
		// GitHub/esa.ioの実測と同じく最小=最大に固定し、サイズ制約をS3の署名検証に転嫁する。
		o.Conditions = []any{
			[]any{"content-length-range", size, size},
		}
	})
	if err != nil {
		return postPolicyForm{}, fmt.Errorf("presign post object: %w", err)
	}
	return postPolicyForm{URL: req.URL, Fields: req.Values}, nil
}

func (a *realS3Adapter) HeadObject(ctx context.Context, bucket, key string) error {
	_, err := a.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("head object: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/upload-server/... -run TestPostPolicyForm -v`
Expected: PASS。ただし`handler.go`/`main.go`が古い`s3Presigner`を参照しているため`go test ./cmd/upload-server/...`全体はこの時点でコンパイルエラーになる。個別テストの実行も同様にパッケージ全体のコンパイルに失敗する場合は、Step 4はスキップしてStep 5（コミット）に進み、Task 4・5完了後にまとめて確認する

- [ ] **Step 5: Commit**

```bash
git add cmd/upload-server/s3_adapter.go cmd/upload-server/s3_adapter_test.go
git commit -m "feat(upload-server): replace presigned PUT with S3 POST policy + HeadObject"
```

---

### Task 4: ハンドラを3エンドポイント構成へ全面書き換え

**Files:**
- Modify: `cmd/upload-server/handler.go`（全面書き換え）
- Modify: `cmd/upload-server/handler_test.go`（全面書き換え）

**Interfaces:**
- Consumes:
  - Task 1: `newConfirmToken(id string, expiresAt time.Time) string`, `verifyConfirmToken(id, token string) bool`
  - Task 2: `assetStore`インターフェース（`Create`, `Get`, `Confirm`）、`assetRecord`、`errAssetNotFound`
  - Task 3: `s3PostPolicyPresigner`インターフェース（`PresignPostPolicy`）、`s3ObjectHeadChecker`インターフェース（`HeadObject`）、`postPolicyForm`
  - 既存: `newObjectKey(userID, contentType string) (string, error)`（`cmd/upload-server/keygen.go`、変更なし）
  - 既存: `cloudFrontSigner`インターフェース（`SignDownloadURL(key string) (string, error)`、`cmd/upload-server/cloudfront_adapter.go`、変更なし）
- Produces:
  - `type uploadServerConfig struct { Bucket string; UploadSecret string; PostExpires time.Duration; ConfirmExpires time.Duration; Store assetStore; S3Presigner s3PostPolicyPresigner; S3HeadChecker s3ObjectHeadChecker; CloudFrontSigner cloudFrontSigner }`
  - `func newUploadServer(cfg uploadServerConfig) *uploadServer`（既存シグネチャ踏襲、フィールドのみ変更）
  - ルーティング: `POST /api/upload/policies`, `PATCH /api/upload/assets/{id}`, `GET /assets/{id}`

- [ ] **Step 1: Write the failing tests for all three endpoints**

```go
// cmd/upload-server/handler_test.go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakePostPolicyPresigner struct {
	form postPolicyForm
	err  error
}

var _ s3PostPolicyPresigner = (*fakePostPolicyPresigner)(nil)

func (f *fakePostPolicyPresigner) PresignPostPolicy(_ context.Context, _, _, _ string, _ int64, _ time.Duration) (postPolicyForm, error) {
	if f.err != nil {
		return postPolicyForm{}, f.err
	}
	return f.form, nil
}

type fakeHeadChecker struct {
	err error
}

var _ s3ObjectHeadChecker = (*fakeHeadChecker)(nil)

func (f *fakeHeadChecker) HeadObject(_ context.Context, _, _ string) error {
	return f.err
}

type fakeCloudFrontSigner struct {
	url string
	err error
}

var _ cloudFrontSigner = (*fakeCloudFrontSigner)(nil)

func (f *fakeCloudFrontSigner) SignDownloadURL(_ string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.url, nil
}

func newTestServer() (*uploadServer, *memoryAssetStore) {
	store := newMemoryAssetStore()
	srv := newUploadServer(uploadServerConfig{
		Bucket:         "test-bucket",
		UploadSecret:   "test-secret",
		PostExpires:    15 * time.Minute,
		ConfirmExpires: 15 * time.Minute,
		Store:          store,
		S3Presigner: &fakePostPolicyPresigner{form: postPolicyForm{
			URL:    "https://bucket.s3.example.com/",
			Fields: map[string]string{"key": "placeholder"},
		}},
		S3HeadChecker:    &fakeHeadChecker{},
		CloudFrontSigner: &fakeCloudFrontSigner{url: "https://cdn.example.com/signed-get"},
	})
	return srv, store
}

func TestUploadPolicies_Success(t *testing.T) {
	srv, _ := newTestServer()
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png","size":22945}`)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/policies", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		ID           string            `json:"id"`
		ConfirmToken string            `json:"confirmToken"`
		Form         struct {
			URL    string            `json:"url"`
			Fields map[string]string `json:"fields"`
		} `json:"form"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !strings.HasPrefix(got.ID, "users/alice/") {
		t.Fatalf("id = %q, want prefix users/alice/", got.ID)
	}
	if got.ConfirmToken == "" {
		t.Fatalf("confirmToken must not be empty")
	}
	if got.Form.URL != "https://bucket.s3.example.com/" {
		t.Fatalf("form.url = %q, unexpected", got.Form.URL)
	}
	if !verifyConfirmToken(got.ID, got.ConfirmToken) {
		t.Fatalf("issued confirmToken does not verify against issued id")
	}
}

func TestUploadPolicies_WrongSecret(t *testing.T) {
	srv, _ := newTestServer()
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png","size":22945}`)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/policies", body)
	req.Header.Set("X-Upload-Secret", "wrong-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestUploadPolicies_InvalidContentType(t *testing.T) {
	srv, _ := newTestServer()
	body := strings.NewReader(`{"userId":"alice","contentType":"application/octet-stream","size":100}`)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/policies", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestUploadPolicies_EmptyUserID(t *testing.T) {
	srv, _ := newTestServer()
	body := strings.NewReader(`{"userId":"","contentType":"image/png","size":100}`)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/policies", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestUploadPolicies_NonPositiveSize(t *testing.T) {
	srv, _ := newTestServer()
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png","size":0}`)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/policies", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestUploadPolicies_MethodNotAllowed(t *testing.T) {
	srv, _ := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/upload/policies", nil)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestUploadPolicies_PresignerError(t *testing.T) {
	store := newMemoryAssetStore()
	srv := newUploadServer(uploadServerConfig{
		Bucket:           "test-bucket",
		UploadSecret:     "test-secret",
		PostExpires:      15 * time.Minute,
		ConfirmExpires:   15 * time.Minute,
		Store:            store,
		S3Presigner:      &fakePostPolicyPresigner{err: errors.New("boom")},
		S3HeadChecker:    &fakeHeadChecker{},
		CloudFrontSigner: &fakeCloudFrontSigner{url: "https://cdn.example.com/signed-get"},
	})
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png","size":100}`)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/policies", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func createTestAsset(t *testing.T, srv *uploadServer) (id, confirmToken string) {
	t.Helper()
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png","size":100}`)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/policies", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()
	srv.ServeMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		ID           string `json:"id"`
		ConfirmToken string `json:"confirmToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("setup: unmarshal response: %v", err)
	}
	return got.ID, got.ConfirmToken
}

func TestConfirmAsset_Success(t *testing.T) {
	srv, store := newTestServer()
	id, token := createTestAsset(t, srv)

	body := strings.NewReader(`{"confirmToken":"` + token + `"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/upload/assets/"+id, body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("store.Get: unexpected error: %v", err)
	}
	if got.ConfirmedAt == nil {
		t.Fatalf("ConfirmedAt = nil, want non-nil after confirm")
	}
}

func TestConfirmAsset_WrongSecret(t *testing.T) {
	srv, _ := newTestServer()
	id, token := createTestAsset(t, srv)

	body := strings.NewReader(`{"confirmToken":"` + token + `"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/upload/assets/"+id, body)
	req.Header.Set("X-Upload-Secret", "wrong-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestConfirmAsset_WrongToken(t *testing.T) {
	srv, _ := newTestServer()
	id, _ := createTestAsset(t, srv)

	body := strings.NewReader(`{"confirmToken":"wrong-token"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/upload/assets/"+id, body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestConfirmAsset_UnknownID(t *testing.T) {
	srv, _ := newTestServer()
	unknownID := "users/alice/does-not-exist.png"
	token := newConfirmToken(unknownID, time.Now().Add(15*time.Minute))

	body := strings.NewReader(`{"confirmToken":"` + token + `"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/upload/assets/"+unknownID, body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestConfirmAsset_HeadObjectFails(t *testing.T) {
	store := newMemoryAssetStore()
	srv := newUploadServer(uploadServerConfig{
		Bucket:         "test-bucket",
		UploadSecret:   "test-secret",
		PostExpires:    15 * time.Minute,
		ConfirmExpires: 15 * time.Minute,
		Store:          store,
		S3Presigner: &fakePostPolicyPresigner{form: postPolicyForm{
			URL:    "https://bucket.s3.example.com/",
			Fields: map[string]string{"key": "placeholder"},
		}},
		S3HeadChecker:    &fakeHeadChecker{err: errors.New("not found in S3")},
		CloudFrontSigner: &fakeCloudFrontSigner{url: "https://cdn.example.com/signed-get"},
	})
	id, token := createTestAsset(t, srv)

	body := strings.NewReader(`{"confirmToken":"` + token + `"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/upload/assets/"+id, body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	got, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("store.Get: unexpected error: %v", err)
	}
	if got.ConfirmedAt != nil {
		t.Fatalf("ConfirmedAt = %v, want nil after failed HeadObject", *got.ConfirmedAt)
	}
}

func TestConfirmAsset_MethodNotAllowed(t *testing.T) {
	srv, _ := newTestServer()
	id, _ := createTestAsset(t, srv)

	req := httptest.NewRequest(http.MethodPost, "/api/upload/assets/"+id, nil)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestGetAsset_RedirectsToSignedURL(t *testing.T) {
	srv, _ := newTestServer()
	id, token := createTestAsset(t, srv)
	confirmReq := httptest.NewRequest(http.MethodPatch, "/api/upload/assets/"+id, strings.NewReader(`{"confirmToken":"`+token+`"}`))
	confirmReq.Header.Set("X-Upload-Secret", "test-secret")
	confirmRec := httptest.NewRecorder()
	srv.ServeMux().ServeHTTP(confirmRec, confirmReq)
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("setup confirm: status = %d, want 200", confirmRec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/assets/"+id, nil)
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "https://cdn.example.com/signed-get" {
		t.Fatalf("Location = %q, unexpected", loc)
	}
}

func TestGetAsset_UnconfirmedReturnsNotFound(t *testing.T) {
	srv, _ := newTestServer()
	id, _ := createTestAsset(t, srv)

	req := httptest.NewRequest(http.MethodGet, "/assets/"+id, nil)
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestGetAsset_UnknownID(t *testing.T) {
	srv, _ := newTestServer()

	req := httptest.NewRequest(http.MethodGet, "/assets/users/alice/does-not-exist.png", nil)
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestGetAsset_SignerError(t *testing.T) {
	store := newMemoryAssetStore()
	srv := newUploadServer(uploadServerConfig{
		Bucket:         "test-bucket",
		UploadSecret:   "test-secret",
		PostExpires:    15 * time.Minute,
		ConfirmExpires: 15 * time.Minute,
		Store:          store,
		S3Presigner: &fakePostPolicyPresigner{form: postPolicyForm{
			URL:    "https://bucket.s3.example.com/",
			Fields: map[string]string{"key": "placeholder"},
		}},
		S3HeadChecker:    &fakeHeadChecker{},
		CloudFrontSigner: &fakeCloudFrontSigner{err: errors.New("boom")},
	})
	id, token := createTestAsset(t, srv)
	confirmReq := httptest.NewRequest(http.MethodPatch, "/api/upload/assets/"+id, strings.NewReader(`{"confirmToken":"`+token+`"}`))
	confirmReq.Header.Set("X-Upload-Secret", "test-secret")
	confirmRec := httptest.NewRecorder()
	srv.ServeMux().ServeHTTP(confirmRec, confirmReq)
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("setup confirm: status = %d, want 200", confirmRec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/assets/"+id, nil)
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/upload-server/... -run "TestUploadPolicies|TestConfirmAsset|TestGetAsset" -v`
Expected: FAIL — compile errors referencing old `handlePresignUpload`/`presignUploadRequest` types still in `handler.go`, and undefined `postPolicyForm`-based config fields

- [ ] **Step 3: Write the implementation**

```go
// cmd/upload-server/handler.go
package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"
)

type uploadServerConfig struct {
	Bucket           string
	UploadSecret     string
	PostExpires      time.Duration
	ConfirmExpires   time.Duration
	Store            assetStore
	S3Presigner      s3PostPolicyPresigner
	S3HeadChecker    s3ObjectHeadChecker
	CloudFrontSigner cloudFrontSigner
}

type uploadServer struct {
	cfg uploadServerConfig
	mux *http.ServeMux
}

func newUploadServer(cfg uploadServerConfig) *uploadServer {
	s := &uploadServer{cfg: cfg, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /api/upload/policies", s.handleUploadPolicies)
	s.mux.HandleFunc("PATCH /api/upload/assets/{id...}", s.handleConfirmAsset)
	s.mux.HandleFunc("GET /assets/{id...}", s.handleGetAsset)
	return s
}

func (s *uploadServer) ServeMux() *http.ServeMux {
	return s.mux
}

func (s *uploadServer) checkSecret(w http.ResponseWriter, r *http.Request) bool {
	got := r.Header.Get("X-Upload-Secret")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.UploadSecret)) != 1 {
		http.Error(w, "invalid or missing X-Upload-Secret header", http.StatusUnauthorized)
		return false
	}
	return true
}

type uploadPoliciesRequest struct {
	UserID      string `json:"userId"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
}

type uploadPoliciesResponse struct {
	ID           string         `json:"id"`
	ConfirmToken string         `json:"confirmToken"`
	Form         postPolicyForm `json:"form"`
}

func (s *uploadServer) handleUploadPolicies(w http.ResponseWriter, r *http.Request) {
	if !s.checkSecret(w, r) {
		return
	}

	var req uploadPoliciesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Size <= 0 {
		http.Error(w, "size must be a positive integer", http.StatusBadRequest)
		return
	}

	id, err := newObjectKey(req.UserID, req.ContentType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	form, err := s.cfg.S3Presigner.PresignPostPolicy(r.Context(), s.cfg.Bucket, id, req.ContentType, req.Size, s.cfg.PostExpires)
	if err != nil {
		http.Error(w, "presign post policy: "+err.Error(), http.StatusInternalServerError)
		return
	}

	now := time.Now()
	rec := assetRecord{
		ID:          id,
		UserID:      req.UserID,
		ContentType: req.ContentType,
		Size:        req.Size,
		CreatedAt:   now,
	}
	if err := s.cfg.Store.Create(r.Context(), rec); err != nil {
		http.Error(w, "create asset record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	token := newConfirmToken(id, now.Add(s.cfg.ConfirmExpires))

	w.Header().Set("Content-Type", "application/json")
	resp := uploadPoliciesResponse{
		ID:           id,
		ConfirmToken: token,
		Form:         form,
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("encode upload-policies response: %v", err)
	}
}

type confirmAssetRequest struct {
	ConfirmToken string `json:"confirmToken"`
}

func (s *uploadServer) handleConfirmAsset(w http.ResponseWriter, r *http.Request) {
	if !s.checkSecret(w, r) {
		return
	}

	id := r.PathValue("id")

	var req confirmAssetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if !verifyConfirmToken(id, req.ConfirmToken) {
		http.Error(w, "invalid or expired confirmToken", http.StatusUnauthorized)
		return
	}

	rec, err := s.cfg.Store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, errAssetNotFound) {
			http.Error(w, "asset not found", http.StatusNotFound)
			return
		}
		http.Error(w, "get asset record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := s.cfg.S3HeadChecker.HeadObject(r.Context(), s.cfg.Bucket, rec.ID); err != nil {
		http.Error(w, "object not found in S3: "+err.Error(), http.StatusNotFound)
		return
	}

	if err := s.cfg.Store.Confirm(r.Context(), id, time.Now()); err != nil {
		http.Error(w, "confirm asset record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *uploadServer) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	rec, err := s.cfg.Store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, errAssetNotFound) {
			http.Error(w, "asset not found", http.StatusNotFound)
			return
		}
		http.Error(w, "get asset record: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if rec.ConfirmedAt == nil {
		http.Error(w, "asset not confirmed", http.StatusNotFound)
		return
	}

	downloadURL, err := s.cfg.CloudFrontSigner.SignDownloadURL(rec.ID)
	if err != nil {
		http.Error(w, "sign download url: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, downloadURL, http.StatusFound)
}
```

**Note on route pattern:** `http.ServeMux`のワイルドカードは`{id...}`のように末尾を`...`にすると`/`を含む残り全体を1つの値としてキャプチャできる（Go1.22+のワイルドカード機能）。`id`自体が`/`を含む（`users/alice/abc123.png`形式）ため、通常の`{id}`（1セグメントのみ）ではなく`{id...}`を使う必要がある。この場合でも`r.PathValue("id")`は`users/alice/abc123.png`をそのまま返す（`net/http/httptest`で動作確認済み）。

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/upload-server/... -v`
Expected: PASS（Task 1-4の全テストが通る。`keygen_test.go`の既存テストも引き続きPASSすること）

- [ ] **Step 5: Run golangci-lint**

Run: `golangci-lint run ./cmd/upload-server/...`
Expected: no findings

- [ ] **Step 6: Commit**

```bash
git add cmd/upload-server/handler.go cmd/upload-server/handler_test.go cmd/upload-server/s3_adapter.go
git commit -m "feat(upload-server): rewrite handlers for policy/confirm/redirect flow"
```

---

### Task 5: main.goの配線更新とweb/index.htmlのフロー刷新

**Files:**
- Modify: `cmd/upload-server/main.go`
- Modify: `web/index.html`

**Interfaces:**
- Consumes:
  - Task 2: `newMemoryAssetStore() *memoryAssetStore`
  - Task 3: `newRealS3Adapter(client *s3.Client) *realS3Adapter`
  - Task 4: `uploadServerConfig{Bucket, UploadSecret, PostExpires, ConfirmExpires, Store, S3Presigner, S3HeadChecker, CloudFrontSigner}`
- Produces: 実行可能な`cmd/upload-server`バイナリ、ブラウザから動作確認できる`web/index.html`

- [ ] **Step 1: Update main.go wiring**

現行の`main.go`（`s3Client := s3.NewFromConfig(awsCfg)` → `newRealS3Presigner(s3Client, *expires)`という配線）を以下に置き換える。`-expires`フラグは「S3署名の有効期限」と「CloudFront署名の有効期限」の両方に使われていたが、Q設計に従い両者を区別する必要はない（`PostExpires`と`ConfirmExpires`をどちらも同じ`*expires`値から設定してよい。検証目的でフラグを増やしすぎない方針に合わせる）。

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
	expires := flag.Duration("expires", 15*time.Minute, "signed URL / confirm token validity duration from now")
	flag.Parse()

	if *bucket == "" || *cloudfrontDomain == "" || *keyPairID == "" || *privateKeyPath == "" || *uploadSecret == "" {
		flag.Usage()
		return fmt.Errorf("missing required flag(s)")
	}
	if *expires <= 0 {
		return fmt.Errorf("-expires must be a positive duration")
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
	s3Adapter := newRealS3Adapter(s3Client)

	urlSigner := sign.NewURLSigner(*keyPairID, signer)

	srv := newUploadServer(uploadServerConfig{
		Bucket:           *bucket,
		UploadSecret:     *uploadSecret,
		PostExpires:      *expires,
		ConfirmExpires:   *expires,
		Store:            newMemoryAssetStore(),
		S3Presigner:      s3Adapter,
		S3HeadChecker:    s3Adapter,
		CloudFrontSigner: newRealCloudFrontSigner(*cloudfrontDomain, urlSigner, *expires),
	})
	srv.ServeMux().Handle("/", http.FileServer(http.Dir("web")))

	fmt.Fprintf(os.Stderr, "listening on %s\n", *addr)
	return http.ListenAndServe(*addr, srv.ServeMux())
}
```

- [ ] **Step 2: Build to verify main.go compiles**

Run: `go build ./cmd/upload-server/...`
Expected: 成功（バイナリ生成物は`.gitignore`済みの`/upload-server`に出力されるので`go build -o /tmp/upload-server-check ./cmd/upload-server`のように一時パスへ出力してもよい）

- [ ] **Step 3: Rewrite web/index.html for POST Policy flow**

```html
<!DOCTYPE html>
<html lang="ja">
<head>
<meta charset="utf-8">
<title>Upload &amp; Signed URL Demo</title>
</head>
<body>
<h1>S3 Upload (POST Policy) -&gt; CloudFront Signed URL Demo</h1>

<label>User ID: <input id="userId" value="alice"></label><br>
<label>Upload Secret: <input id="secret" value="DONT_USE_THIS_CODE"></label><br>
<input type="file" id="file" accept="image/*"><br>
<button id="uploadBtn">Upload</button>

<pre id="log"></pre>
<a id="result" href="#" target="_blank" style="display:none">Open asset (redirects to signed URL)</a>

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

  log('upload policies を要求中...');
  const policiesRes = await fetch('/api/upload/policies', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Upload-Secret': secret,
    },
    body: JSON.stringify({ userId: userId, contentType: file.type, size: file.size }),
  });
  if (!policiesRes.ok) {
    log('upload policies 失敗: ' + policiesRes.status);
    return;
  }
  const { id, confirmToken, form } = await policiesRes.json();
  log('id = ' + id);

  log('S3へPOST中...');
  const formData = new FormData();
  for (const [key, value] of Object.entries(form.fields)) {
    formData.append(key, value);
  }
  formData.append('file', file);
  const postRes = await fetch(form.url, {
    method: 'POST',
    body: formData,
  });
  if (!postRes.ok) {
    log('S3 POST 失敗: ' + postRes.status);
    return;
  }
  log('S3 POST 成功 (status=' + postRes.status + ')');

  log('完了通知を送信中...');
  const confirmRes = await fetch('/api/upload/assets/' + id, {
    method: 'PATCH',
    headers: {
      'Content-Type': 'application/json',
      'X-Upload-Secret': secret,
    },
    body: JSON.stringify({ confirmToken: confirmToken }),
  });
  if (!confirmRes.ok) {
    log('完了通知 失敗: ' + confirmRes.status);
    return;
  }
  log('完了通知 成功');

  const a = document.getElementById('result');
  a.href = '/assets/' + id;
  a.style.display = 'inline';
  a.textContent = '/assets/' + id;
});
</script>
</body>
</html>
```

- [ ] **Step 4: Run full test suite**

Run: `go test ./...`
Expected: PASS（全パッケージ）

- [ ] **Step 5: Run golangci-lint**

Run: `golangci-lint run ./...`
Expected: no findings

- [ ] **Step 6: Commit**

```bash
git add cmd/upload-server/main.go web/index.html
git commit -m "feat(upload-server): wire policy/confirm flow into main and browser demo"
```

---

### Task 6: 手動E2E検証（AWS実環境）とREADME更新

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: Task 1-5で完成した`cmd/upload-server`バイナリ、既存のTerraformインフラ（`terraform/`ディレクトリ、Task外で別途`apply`済みであることが前提）

このタスクはコード変更を主とせず、実際にAWS環境で動作確認を行った上でREADMEに検証結果を記録するタスクである。AWS認証情報とTerraform適用済みインフラを持つ実行者（ユーザー本人の環境）でのみ実施可能なため、サブエージェントに委譲せずユーザー自身が実施する。

- [ ] **Step 1: サーバーを起動する**

```bash
go run ./cmd/upload-server \
  -bucket <your-bucket-name> \
  -cloudfront-domain <your-cloudfront-domain> \
  -key-pair-id <your-key-pair-id> \
  -private-key <path-to-pkcs8-pem> \
  -upload-secret DONT_USE_THIS_CODE
```

- [ ] **Step 2: ブラウザで`http://localhost:8080`を開き、画像をアップロードする**

期待される挙動: ログに`upload policies`→`S3へPOST`→`完了通知`の成功ログが順に表示され、最後に`/assets/{id}`リンクが表示される

- [ ] **Step 3: `/assets/{id}`リンクをクリックし、CloudFront Signed URLへ302リダイレクトされ画像が表示されることを確認する**

- [ ] **Step 4: 異常系を手動確認する**

- 誤った`confirmToken`で`PATCH`した場合に401が返ること
- 完了通知前に`GET /assets/{id}`した場合に404が返ること（Network タブで確認）
- Content-Typeが`image/`以外のファイルを送ろうとした場合に400で拒否されること

- [ ] **Step 5: README.mdに検証結果を追記する**

既存のREADMEの構成（検証内容の箇条書き）に、今回のPOST Policy方式への刷新と確認できた項目を追記する。具体的な文面は確認結果に応じてユーザーと相談の上で決定する（このステップはコード変更ではなくドキュメント更新であり、確認結果次第で内容が変わるため、この計画では文面を先に固定しない）。

- [ ] **Step 6: Commit**

```bash
git add README.md
git commit -m "docs: record GitHub/esa.io-style upload flow verification results"
```
