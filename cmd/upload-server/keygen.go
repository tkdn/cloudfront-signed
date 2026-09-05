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
	errInvalidUserID      = errors.New("userId must not contain '/'")
	errInvalidContentType = errors.New("contentType must start with image/")
)

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

	ext := ""
	if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
		ext = exts[0]
	}

	// 16 bytes of entropy for key uniqueness, not a UUID format requirement.
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random id: %w", err)
	}

	return fmt.Sprintf("users/%s/%s%s", userID, hex.EncodeToString(buf), ext), nil
}
