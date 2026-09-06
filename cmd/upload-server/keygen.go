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
	errEmptyUserID         = errors.New("userId must not be empty")
	errInvalidUserID       = errors.New("userId must not contain '/'")
	errInvalidContentType  = errors.New("contentType must start with image/")
	errUnresolvableFileExt = errors.New("could not determine a file extension for the given contentType")
)

// knownImageExtensionsは主要な画像形式の拡張子を固定する。mime.ExtensionsByTypeの
// 結果はホストの/etc/mime.types等に依存し、例えば"image/jpeg"に".jfif"を返す環境がある。
var knownImageExtensions = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

func newObjectKey(userID, contentType string) (string, error) {
	if userID == "" {
		return "", errEmptyUserID
	}
	if strings.Contains(userID, "/") {
		return "", errInvalidUserID
	}
	if !strings.HasPrefix(contentType, "image/") {
		return "", errInvalidContentType
	}

	ext, ok := knownImageExtensions[contentType]
	if !ok {
		if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
			ext = exts[0]
		}
	}
	if ext == "" {
		return "", errUnresolvableFileExt
	}

	// キーの一意性のための16バイトのエントロピーであり、UUID形式である必要はない。
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random id: %w", err)
	}

	return fmt.Sprintf("users/%s/%s%s", userID, hex.EncodeToString(buf), ext), nil
}
