# 環境変数化とgoogle/wire導入 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `cmd/upload-server`の設定読み込みをコマンドラインflagから環境変数専用に全面刷新し、`google/wire`によるコンパイル時DIで一部の依存解決を組み立てる。

**Architecture:** 設定値（bucket名・シークレット等）は`os.Getenv`のみで読み込む`loadConfig()`関数に集約し、flagパッケージへの依存を完全に排除する。AWS SDK・署名鍵の初期化は`main.go`にそのまま残し、そこから先（`s3PostPolicyPresigner`/`s3ObjectHeadChecker`/`cloudFrontSigner`/`assetStore`という4つのインターフェースの解決）だけを`google/wire`が生成する`initializeUploadServerDeps`関数に任せる。

**Tech Stack:** Go 1.27.1、`google/wire` v0.7.0（`go get -tool`でtool dependencyとして管理）、標準ライブラリ`os`/`time`。

**Spec:** このplanは会話履歴中のgrillingセッション（2026-09-06、本ファイルの直前の会話ターン群）で確定した設計をspecとして扱う。

## Global Constraints

- Go 1.27.1
- 各タスク完了時に`go test ./...`と`golangci-lint run ./...`を通す
- コメントは「なぜ」が非自明な箇所にのみ1行で書く。「何をしているか」の説明は書かない
- 検証目的のリポジトリであり自動E2Eテストは実装しない。ユニットテストのみTDDで書く
- 全てのインターフェース実装に`var _ Interface = (*Type)(nil)`のコンパイル時アサーションを付ける（既存パターン踏襲）
- スコープは`cmd/upload-server`のみ。`cmd/sign-cli`とREADMEは対象外
- 既存の`cmd/upload-server/handler.go`, `store.go`, `token.go`, `s3_adapter.go`, `cloudfront_adapter.go`, `keygen.go`は変更しない

---

### Task 1: 環境変数からの設定読み込み

**Files:**
- Create: `cmd/upload-server/config.go`
- Test: `cmd/upload-server/config_test.go`

**Interfaces:**
- Consumes: なし（標準ライブラリの`os`, `time`, `fmt`, `strings`, `errors`のみ）
- Produces:
  - `type config struct { Addr, Bucket, CloudFrontDomain, KeyPairID, PrivateKeyPath, UploadSecret string; Expires time.Duration }`
  - `func loadConfig() (config, error)` — `os.Getenv`で7つの環境変数を読み、`config`を返す。必須変数の不足はまとめて1つのエラーで報告し、`UPLOAD_SERVER_EXPIRES`のパースエラーは別のエラーメッセージにする

環境変数とフィールドの対応:
- `UPLOAD_SERVER_ADDR` → `Addr`（未設定時デフォルト `":8080"`、必須ではない）
- `UPLOAD_SERVER_BUCKET` → `Bucket`（必須）
- `UPLOAD_SERVER_CLOUDFRONT_DOMAIN` → `CloudFrontDomain`（必須）
- `UPLOAD_SERVER_KEY_PAIR_ID` → `KeyPairID`（必須）
- `UPLOAD_SERVER_PRIVATE_KEY` → `PrivateKeyPath`（必須）
- `UPLOAD_SERVER_UPLOAD_SECRET` → `UploadSecret`（必須）
- `UPLOAD_SERVER_EXPIRES` → `Expires`（未設定時デフォルト `15*time.Minute`、設定時は`time.ParseDuration`でパース。パース後の値が0以下ならエラー）

エラーメッセージの正確な形式:
- 必須変数が1つ以上未設定: `"missing required environment variable(s): "` + カンマ区切りの環境変数名（`UPLOAD_SERVER_BUCKET, UPLOAD_SERVER_CLOUDFRONT_DOMAIN`のように、未設定のものを検出順に列挙）
- `UPLOAD_SERVER_EXPIRES`のパース失敗: `fmt.Errorf("invalid UPLOAD_SERVER_EXPIRES: %w", err)`（`err`は`time.ParseDuration`が返したエラー）
- `UPLOAD_SERVER_EXPIRES`のパース成功だが0以下: `errors.New("UPLOAD_SERVER_EXPIRES must be a positive duration")`

- [ ] **Step 1: Write the failing tests**

```go
// cmd/upload-server/config_test.go
package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func clearUploadServerEnv(t *testing.T) {
	t.Helper()
	vars := []string{
		"UPLOAD_SERVER_ADDR",
		"UPLOAD_SERVER_BUCKET",
		"UPLOAD_SERVER_CLOUDFRONT_DOMAIN",
		"UPLOAD_SERVER_KEY_PAIR_ID",
		"UPLOAD_SERVER_PRIVATE_KEY",
		"UPLOAD_SERVER_UPLOAD_SECRET",
		"UPLOAD_SERVER_EXPIRES",
	}
	for _, v := range vars {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
}

func setAllRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("UPLOAD_SERVER_BUCKET", "test-bucket")
	t.Setenv("UPLOAD_SERVER_CLOUDFRONT_DOMAIN", "test.cloudfront.net")
	t.Setenv("UPLOAD_SERVER_KEY_PAIR_ID", "K123")
	t.Setenv("UPLOAD_SERVER_PRIVATE_KEY", "keys/private_key.pem")
	t.Setenv("UPLOAD_SERVER_UPLOAD_SECRET", "test-secret")
}

func TestLoadConfig_Success(t *testing.T) {
	clearUploadServerEnv(t)
	setAllRequiredEnv(t)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Fatalf("Addr = %q, want default \":8080\"", cfg.Addr)
	}
	if cfg.Bucket != "test-bucket" {
		t.Fatalf("Bucket = %q, want %q", cfg.Bucket, "test-bucket")
	}
	if cfg.CloudFrontDomain != "test.cloudfront.net" {
		t.Fatalf("CloudFrontDomain = %q, want %q", cfg.CloudFrontDomain, "test.cloudfront.net")
	}
	if cfg.KeyPairID != "K123" {
		t.Fatalf("KeyPairID = %q, want %q", cfg.KeyPairID, "K123")
	}
	if cfg.PrivateKeyPath != "keys/private_key.pem" {
		t.Fatalf("PrivateKeyPath = %q, want %q", cfg.PrivateKeyPath, "keys/private_key.pem")
	}
	if cfg.UploadSecret != "test-secret" {
		t.Fatalf("UploadSecret = %q, want %q", cfg.UploadSecret, "test-secret")
	}
	if cfg.Expires != 15*time.Minute {
		t.Fatalf("Expires = %v, want default 15m", cfg.Expires)
	}
}

func TestLoadConfig_CustomAddrAndExpires(t *testing.T) {
	clearUploadServerEnv(t)
	setAllRequiredEnv(t)
	t.Setenv("UPLOAD_SERVER_ADDR", ":9090")
	t.Setenv("UPLOAD_SERVER_EXPIRES", "30m")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Addr != ":9090" {
		t.Fatalf("Addr = %q, want %q", cfg.Addr, ":9090")
	}
	if cfg.Expires != 30*time.Minute {
		t.Fatalf("Expires = %v, want 30m", cfg.Expires)
	}
}

func TestLoadConfig_MissingAllRequired(t *testing.T) {
	clearUploadServerEnv(t)

	_, err := loadConfig()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "missing required environment variable(s)") {
		t.Fatalf("err = %v, want message containing %q", err, "missing required environment variable(s)")
	}
	for _, want := range []string{
		"UPLOAD_SERVER_BUCKET",
		"UPLOAD_SERVER_CLOUDFRONT_DOMAIN",
		"UPLOAD_SERVER_KEY_PAIR_ID",
		"UPLOAD_SERVER_PRIVATE_KEY",
		"UPLOAD_SERVER_UPLOAD_SECRET",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to mention %q", err, want)
		}
	}
}

func TestLoadConfig_MissingOneRequired(t *testing.T) {
	clearUploadServerEnv(t)
	setAllRequiredEnv(t)
	os.Unsetenv("UPLOAD_SERVER_BUCKET")

	_, err := loadConfig()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "UPLOAD_SERVER_BUCKET") {
		t.Fatalf("err = %v, want it to mention UPLOAD_SERVER_BUCKET", err)
	}
	if strings.Contains(err.Error(), "UPLOAD_SERVER_UPLOAD_SECRET") {
		t.Fatalf("err = %v, should not mention UPLOAD_SERVER_UPLOAD_SECRET (it was set)", err)
	}
}

func TestLoadConfig_InvalidExpires(t *testing.T) {
	clearUploadServerEnv(t)
	setAllRequiredEnv(t)
	t.Setenv("UPLOAD_SERVER_EXPIRES", "not-a-duration")

	_, err := loadConfig()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid UPLOAD_SERVER_EXPIRES") {
		t.Fatalf("err = %v, want message containing %q", err, "invalid UPLOAD_SERVER_EXPIRES")
	}
}

func TestLoadConfig_NonPositiveExpires(t *testing.T) {
	clearUploadServerEnv(t)
	setAllRequiredEnv(t)
	t.Setenv("UPLOAD_SERVER_EXPIRES", "0s")

	_, err := loadConfig()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "positive duration") {
		t.Fatalf("err = %v, want message containing %q", err, "positive duration")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/upload-server/... -run TestLoadConfig -v`
Expected: FAIL with `undefined: loadConfig` (compile error)

- [ ] **Step 3: Write minimal implementation**

```go
// cmd/upload-server/config.go
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type config struct {
	Addr             string
	Bucket           string
	CloudFrontDomain string
	KeyPairID        string
	PrivateKeyPath   string
	UploadSecret     string
	Expires          time.Duration
}

const defaultExpires = 15 * time.Minute

func loadConfig() (config, error) {
	cfg := config{
		Addr:             os.Getenv("UPLOAD_SERVER_ADDR"),
		Bucket:           os.Getenv("UPLOAD_SERVER_BUCKET"),
		CloudFrontDomain: os.Getenv("UPLOAD_SERVER_CLOUDFRONT_DOMAIN"),
		KeyPairID:        os.Getenv("UPLOAD_SERVER_KEY_PAIR_ID"),
		PrivateKeyPath:   os.Getenv("UPLOAD_SERVER_PRIVATE_KEY"),
		UploadSecret:     os.Getenv("UPLOAD_SERVER_UPLOAD_SECRET"),
	}
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}

	var missing []string
	if cfg.Bucket == "" {
		missing = append(missing, "UPLOAD_SERVER_BUCKET")
	}
	if cfg.CloudFrontDomain == "" {
		missing = append(missing, "UPLOAD_SERVER_CLOUDFRONT_DOMAIN")
	}
	if cfg.KeyPairID == "" {
		missing = append(missing, "UPLOAD_SERVER_KEY_PAIR_ID")
	}
	if cfg.PrivateKeyPath == "" {
		missing = append(missing, "UPLOAD_SERVER_PRIVATE_KEY")
	}
	if cfg.UploadSecret == "" {
		missing = append(missing, "UPLOAD_SERVER_UPLOAD_SECRET")
	}
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing required environment variable(s): %s", strings.Join(missing, ", "))
	}

	expiresStr := os.Getenv("UPLOAD_SERVER_EXPIRES")
	if expiresStr == "" {
		cfg.Expires = defaultExpires
	} else {
		expires, err := time.ParseDuration(expiresStr)
		if err != nil {
			return config{}, fmt.Errorf("invalid UPLOAD_SERVER_EXPIRES: %w", err)
		}
		cfg.Expires = expires
	}
	if cfg.Expires <= 0 {
		return config{}, errors.New("UPLOAD_SERVER_EXPIRES must be a positive duration")
	}

	return cfg, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/upload-server/... -run TestLoadConfig -v`
Expected: PASS (all 6 subtests)

- [ ] **Step 5: Run golangci-lint**

Run: `golangci-lint run ./cmd/upload-server/...`
Expected: no findings

- [ ] **Step 6: Commit**

```bash
git add cmd/upload-server/config.go cmd/upload-server/config_test.go
git commit -m "feat(upload-server): load configuration from environment variables"
```

---

### Task 2: google/wireの導入と依存解決の生成

**Files:**
- Modify: `go.mod`, `go.sum`（`go get -tool`で自動更新）
- Create: `cmd/upload-server/wire.go`
- Create: `cmd/upload-server/wire_gen.go`（`wire`コマンドで生成、手動編集しない）

**Interfaces:**
- Consumes:
  - 既存: `newRealS3Adapter(client *s3.Client) *realS3Adapter`（`cmd/upload-server/s3_adapter.go`）が`s3PostPolicyPresigner`と`s3ObjectHeadChecker`の両方を満たす
  - 既存: `newRealCloudFrontSigner(domain string, signer *sign.URLSigner, expires time.Duration) *realCloudFrontSigner`（`cmd/upload-server/cloudfront_adapter.go`）が`cloudFrontSigner`を満たす
  - 既存: `newMemoryAssetStore() *memoryAssetStore`（`cmd/upload-server/store.go`）が`assetStore`を満たす
- Produces:
  - `type serverDeps struct { S3Presigner s3PostPolicyPresigner; S3HeadChecker s3ObjectHeadChecker; CloudFrontSigner cloudFrontSigner; Store assetStore }`
  - `func initializeUploadServerDeps(s3Client *s3.Client, urlSigner *sign.URLSigner, cloudfrontDomain string, expires time.Duration) serverDeps`（wire_gen.goに生成される、エラーを返さない）

このタスクはコード生成を伴う特殊なタスクである。手順は以下の通り厳密に実施すること。

- [ ] **Step 1: wireをtool dependencyとして追加する**

Run: `go get -tool github.com/google/wire/cmd/wire@v0.7.0`

Expected: `go.mod`に以下の行が追加される
```
require github.com/google/wire v0.7.0
```
と、ファイル末尾付近に
```
tool github.com/google/wire/cmd/wire
```
が追加される。`go.sum`にも対応するエントリが追加される。バージョンは`v0.7.0`を明示的に指定すること（`go get -tool github.com/google/wire/cmd/wire`だけだと最新版が入り、後続の生成コードの形が変わる可能性がある）。

- [ ] **Step 2: wire.goを作成する**

```go
// cmd/upload-server/wire.go
//go:build wireinject

package main

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/wire"
)

type serverDeps struct {
	S3Presigner      s3PostPolicyPresigner
	S3HeadChecker    s3ObjectHeadChecker
	CloudFrontSigner cloudFrontSigner
	Store            assetStore
}

func initializeUploadServerDeps(s3Client *s3.Client, urlSigner *sign.URLSigner, cloudfrontDomain string, expires time.Duration) serverDeps {
	wire.Build(
		wire.Struct(new(serverDeps), "*"),
		newRealS3Adapter,
		wire.Bind(new(s3PostPolicyPresigner), new(*realS3Adapter)),
		wire.Bind(new(s3ObjectHeadChecker), new(*realS3Adapter)),
		newRealCloudFrontSigner,
		wire.Bind(new(cloudFrontSigner), new(*realCloudFrontSigner)),
		newMemoryAssetStore,
		wire.Bind(new(assetStore), new(*memoryAssetStore)),
	)
	return serverDeps{}
}
```

**重要**: `wire.go`はビルドタグ`wireinject`が付いているため、通常のビルド（`go build`, `go test`）では除外される。この時点で`go build ./cmd/upload-server/...`を実行しても、`wire.go`はコンパイル対象に含まれないため、`serverDeps`や`initializeUploadServerDeps`が未定義でもエラーにならない。ただし`main.go`（Task 3で書き換え予定）がまだ`serverDeps`や`initializeUploadServerDeps`を参照していない現時点では、パッケージ全体のコンパイルは変わらず成功するはずである。

- [ ] **Step 3: wireコマンドを実行してwire_gen.goを生成する**

Run: `go tool wire ./cmd/upload-server/...`

Expected: `wire: <module>: wrote .../cmd/upload-server/wire_gen.go`というメッセージが出力され、以下の内容の`cmd/upload-server/wire_gen.go`が生成される（変数名のサフィックス数字等、細部はwireのバージョンや環境で変わりうるが、関数シグネチャと処理内容は以下と一致するはずである）:

```go
// Code generated by Wire. DO NOT EDIT.

//go:generate go run -mod=mod github.com/google/wire/cmd/wire
//go:build !wireinject

package main

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Injectors from wire.go:

func initializeUploadServerDeps(s3Client *s3.Client, urlSigner *sign.URLSigner, cloudfrontDomain string, expires time.Duration) serverDeps {
	realS3Adapter := newRealS3Adapter(s3Client)
	realCloudFrontSigner := newRealCloudFrontSigner(cloudfrontDomain, urlSigner, expires)
	memoryAssetStore := newMemoryAssetStore()
	deps := serverDeps{
		S3Presigner:      realS3Adapter,
		S3HeadChecker:    realS3Adapter,
		CloudFrontSigner: realCloudFrontSigner,
		Store:            memoryAssetStore,
	}
	return deps
}
```

もし生成結果がこれと異なる場合（変数名や順序が違うだけであれば問題ない。関数シグネチャが違う、エラーを返す形になっている、`S3Presigner`と`S3HeadChecker`に別々のインスタンスが入っている、等の構造的な差異がある場合は問題）、生成された実際のファイルを正としてこのタスクを完了し、レポートに差異を明記すること。

- [ ] **Step 4: ビルド確認**

Run: `go build ./cmd/upload-server/...`
Expected: 成功（`wire_gen.go`は`!wireinject`タグにより通常ビルドに含まれ、`wire.go`は除外される）

Run: `go vet ./cmd/upload-server/...`
Expected: no errors

Run: `go test ./...`
Expected: 既存のテストが全て成功（このタスクでは新規のテストコードは書かない。`initializeUploadServerDeps`はTask 3でmain.goから呼ばれて初めて使われる）

- [ ] **Step 5: golangci-lintを確認する**

Run: `golangci-lint run ./...`

`wire_gen.go`は生成コードのため、lintで問題が出る場合がある。もし`wire_gen.go`由来の指摘が出た場合はレポートに記載すること（生成コードなので手動修正はしない。設定で除外が必要か検討が要る場合はレポートで報告し、このタスクの完了条件からは除く）。既存コード（`s3_adapter.go`等）由来の新規の指摘がないことを確認する。

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum cmd/upload-server/wire.go cmd/upload-server/wire_gen.go
git commit -m "feat(upload-server): introduce google/wire for dependency wiring"
```

---

### Task 3: main.goの全面刷新

**Files:**
- Modify: `cmd/upload-server/main.go`

**Interfaces:**
- Consumes:
  - Task 1: `type config struct {...}`, `func loadConfig() (config, error)`
  - Task 2: `type serverDeps struct {...}`, `func initializeUploadServerDeps(s3Client *s3.Client, urlSigner *sign.URLSigner, cloudfrontDomain string, expires time.Duration) serverDeps`
  - 既存: `uploadServerConfig{Bucket, UploadSecret, PostExpires, ConfirmExpires, StaticDir, Store, S3Presigner, S3HeadChecker, CloudFrontSigner}`, `newUploadServer(cfg uploadServerConfig) *uploadServer`（`cmd/upload-server/handler.go`）

- [ ] **Step 1: main.goを書き換える**

現行の`main.go`（flag定義、`flag.Usage()`、`*bucket == ""`等のバリデーション）を全て削除し、以下に置き換える:

```go
// cmd/upload-server/main.go
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

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
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	f, err := os.Open(cfg.PrivateKeyPath)
	if err != nil {
		return fmt.Errorf("open private key: %w", err)
	}
	defer func() { _ = f.Close() }()

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
	urlSigner := sign.NewURLSigner(cfg.KeyPairID, signer)

	deps := initializeUploadServerDeps(s3Client, urlSigner, cfg.CloudFrontDomain, cfg.Expires)

	srv := newUploadServer(uploadServerConfig{
		Bucket:           cfg.Bucket,
		UploadSecret:     cfg.UploadSecret,
		PostExpires:      cfg.Expires,
		ConfirmExpires:   cfg.Expires,
		StaticDir:        "web",
		Store:            deps.Store,
		S3Presigner:      deps.S3Presigner,
		S3HeadChecker:    deps.S3HeadChecker,
		CloudFrontSigner: deps.CloudFrontSigner,
	})

	fmt.Fprintf(os.Stderr, "listening on %s\n", cfg.Addr)
	return http.ListenAndServe(cfg.Addr, srv.ServeMux())
}
```

`-key-pair-id`は`cfg.KeyPairID`としてTask 1の`config`構造体に既に含まれている。`flag`パッケージのimportは完全に削除されていることを確認する。

- [ ] **Step 2: ビルドとテストを確認する**

Run: `go build ./...`
Expected: 成功

Run: `go test ./...`
Expected: 全パッケージ成功

Run: `go vet ./...`
Expected: no errors

- [ ] **Step 3: golangci-lintを確認する**

Run: `golangci-lint run ./...`
Expected: no new findings（既知の事前登録済みfindingsがあれば無視してよいが、このタスクで新規に導入したものがないことを確認する）

- [ ] **Step 4: 手動での起動確認（環境変数がなくてもエラーメッセージが正しく出ることの確認）**

Run: `go run ./cmd/upload-server 2>&1 | head -5`
Expected: `missing required environment variable(s): UPLOAD_SERVER_BUCKET, UPLOAD_SERVER_CLOUDFRONT_DOMAIN, UPLOAD_SERVER_KEY_PAIR_ID, UPLOAD_SERVER_PRIVATE_KEY, UPLOAD_SERVER_UPLOAD_SECRET`のようなエラーメッセージが出力され、プロセスが終了コード1で終わることを確認する（実際にAWS環境に接続する必要はない。この時点で`loadConfig()`のエラーによって早期returnするため、AWS呼び出しには到達しない）。

- [ ] **Step 5: Commit**

```bash
git add cmd/upload-server/main.go
git commit -m "feat(upload-server): wire environment-based config and generated DI into main"
```
