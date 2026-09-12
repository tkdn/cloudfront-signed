package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// mockS3Adapterはローカルファイルシステムを実体ストレージとして扱う
// s3PostPolicyPresigner / s3ObjectHeadCheckerの検証用実装。
type mockS3Adapter struct {
	rootDir string
}

var _ s3PostPolicyPresigner = (*mockS3Adapter)(nil)
var _ s3ObjectHeadChecker = (*mockS3Adapter)(nil)

func newMockS3Adapter(cfg config) *mockS3Adapter {
	return &mockS3Adapter{rootDir: cfg.MockStorageDir}
}

// PresignPostPolicyはS3への署名付きPOSTの代わりに、keyとcontentTypeのみを
// フィールドに含むpostPolicyFormを返す。呼び出し先URLの組み立ては
// mockRouteRegistrarが担う。
func (a *mockS3Adapter) PresignPostPolicy(_ context.Context, _, key, contentType string, _ int64, _ time.Duration) (postPolicyForm, error) {
	return postPolicyForm{
		Fields: map[string]string{
			"key":          key,
			"Content-Type": contentType,
		},
	}, nil
}

// HeadObjectはkeyに対応するローカルファイルの存在をos.Statで確認する。
func (a *mockS3Adapter) HeadObject(_ context.Context, _, key string) error {
	path, err := a.resolvePath(key)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("stat mock object: %w", err)
	}
	return nil
}

// Saveはkeyに対応するローカルパスへdataを書き込む。
func (a *mockS3Adapter) Save(key string, data []byte) error {
	path, err := a.resolvePath(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create mock storage dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write mock object: %w", err)
	}
	return nil
}

// Openはkeyに対応するローカルファイルを読み出し用に開く。呼び出し側でCloseすること。
func (a *mockS3Adapter) Open(key string) (*os.File, error) {
	path, err := a.resolvePath(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open mock object: %w", err)
	}
	return f, nil
}

// resolvePathはkeyをrootDir配下の絶対パスへ変換し、パストラバーサルで
// rootDirの外に出ないことを確認する。
func (a *mockS3Adapter) resolvePath(key string) (string, error) {
	cleanKey := filepath.Clean(filepath.FromSlash(key))
	if cleanKey == ".." || strings.HasPrefix(cleanKey, ".."+string(filepath.Separator)) || filepath.IsAbs(cleanKey) {
		return "", fmt.Errorf("invalid object key: %q", key)
	}
	root, err := filepath.Abs(a.rootDir)
	if err != nil {
		return "", fmt.Errorf("resolve mock storage root: %w", err)
	}
	path := filepath.Join(root, cleanKey)
	if !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return "", fmt.Errorf("object key escapes mock storage root: %q", key)
	}
	return path, nil
}
