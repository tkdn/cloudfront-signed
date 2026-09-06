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

func TestMemoryAssetStore_ConfirmIdempotent(t *testing.T) {
	store := newMemoryAssetStore()
	ctx := context.Background()
	rec := assetRecord{ID: "users/alice/abc123.png", UserID: "alice", ContentType: "image/png", Size: 100, CreatedAt: time.Now()}
	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("Create: unexpected error: %v", err)
	}

	firstConfirm := time.Now()
	if err := store.Confirm(ctx, rec.ID, firstConfirm); err != nil {
		t.Fatalf("first Confirm: unexpected error: %v", err)
	}

	secondConfirm := firstConfirm.Add(time.Hour)
	if err := store.Confirm(ctx, rec.ID, secondConfirm); err != nil {
		t.Fatalf("second Confirm: unexpected error: %v", err)
	}

	got, err := store.Get(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if got.ConfirmedAt == nil {
		t.Fatalf("ConfirmedAt = nil, want non-nil")
	}
	if !got.ConfirmedAt.Equal(firstConfirm) {
		t.Fatalf("ConfirmedAt = %v, want first confirm time %v (should not be overwritten by second call)", *got.ConfirmedAt, firstConfirm)
	}
}

func TestMemoryAssetStore_CreateDuplicateID(t *testing.T) {
	store := newMemoryAssetStore()
	ctx := context.Background()
	rec := assetRecord{ID: "users/alice/abc123.png", UserID: "alice", ContentType: "image/png", Size: 100, CreatedAt: time.Now()}
	if err := store.Create(ctx, rec); err != nil {
		t.Fatalf("first Create: unexpected error: %v", err)
	}

	err := store.Create(ctx, rec)
	if !errors.Is(err, errAssetAlreadyExists) {
		t.Fatalf("second Create: err = %v, want errAssetAlreadyExists", err)
	}
}
