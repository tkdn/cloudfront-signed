package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMockS3Adapter_SaveThenHeadObject_Success(t *testing.T) {
	dir := t.TempDir()
	a := newMockS3Adapter(config{MockStorageDir: dir})

	if err := a.Save("users/alice/foo.png", []byte("data")); err != nil {
		t.Fatalf("Save: unexpected error: %v", err)
	}

	if err := a.HeadObject(context.Background(), "bucket", "users/alice/foo.png"); err != nil {
		t.Fatalf("HeadObject: unexpected error: %v", err)
	}
}

func TestMockS3Adapter_HeadObject_NotFound(t *testing.T) {
	dir := t.TempDir()
	a := newMockS3Adapter(config{MockStorageDir: dir})

	if err := a.HeadObject(context.Background(), "bucket", "users/alice/missing.png"); err == nil {
		t.Fatalf("HeadObject: want error for missing object, got nil")
	}
}

func TestMockS3Adapter_SaveThenOpen_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	a := newMockS3Adapter(config{MockStorageDir: dir})
	want := []byte("hello world")

	if err := a.Save("users/alice/foo.png", want); err != nil {
		t.Fatalf("Save: unexpected error: %v", err)
	}

	f, err := a.Open("users/alice/foo.png")
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
	defer func() { _ = f.Close() }()

	got := make([]byte, len(want))
	if _, err := f.Read(got); err != nil {
		t.Fatalf("Read: unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("round trip = %q, want %q", got, want)
	}
}

func TestMockS3Adapter_Open_NotFound(t *testing.T) {
	dir := t.TempDir()
	a := newMockS3Adapter(config{MockStorageDir: dir})

	if _, err := a.Open("users/alice/missing.png"); err == nil {
		t.Fatalf("Open: want error for missing object, got nil")
	}
}

func TestMockS3Adapter_ResolvePath_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	a := newMockS3Adapter(config{MockStorageDir: dir})

	cases := []string{
		"../escape.png",
		"users/../../escape.png",
		"/etc/passwd",
	}
	for _, key := range cases {
		if _, err := a.resolvePath(key); err == nil {
			t.Fatalf("resolvePath(%q): want error, got nil", key)
		}
	}
}

func TestMockS3Adapter_Save_CreatesNestedDirectories(t *testing.T) {
	dir := t.TempDir()
	a := newMockS3Adapter(config{MockStorageDir: dir})

	if err := a.Save("users/alice/nested/foo.png", []byte("data")); err != nil {
		t.Fatalf("Save: unexpected error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "users", "alice", "nested", "foo.png")); err != nil {
		t.Fatalf("expected file to exist on disk: %v", err)
	}
}

func TestMockS3Adapter_PresignPostPolicy_ReturnsKeyAndContentType(t *testing.T) {
	dir := t.TempDir()
	a := newMockS3Adapter(config{MockStorageDir: dir})

	form, err := a.PresignPostPolicy(context.Background(), "bucket", "users/alice/foo.png", "image/png", 100, 0)
	if err != nil {
		t.Fatalf("PresignPostPolicy: unexpected error: %v", err)
	}
	if form.Fields["key"] != "users/alice/foo.png" {
		t.Fatalf("Fields[key] = %q, want %q", form.Fields["key"], "users/alice/foo.png")
	}
	if form.Fields["Content-Type"] != "image/png" {
		t.Fatalf("Fields[Content-Type] = %q, want %q", form.Fields["Content-Type"], "image/png")
	}
}
