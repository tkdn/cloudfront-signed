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
