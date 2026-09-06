package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	errAssetNotFound      = errors.New("asset record not found")
	errAssetAlreadyExists = errors.New("asset record already exists")
)

type assetRecord struct {
	ID          string
	UserID      string
	ContentType string
	Size        int64
	CreatedAt   time.Time
	ConfirmedAt *time.Time
}

type assetStore interface {
	// idが既に存在する場合はerrAssetAlreadyExistsを返す。
	Create(ctx context.Context, rec assetRecord) error
	Get(ctx context.Context, id string) (assetRecord, error)
	// 未確定の場合のみConfirmedAtを設定する。確定済みレコードへの呼び出しは
	// 冪等な成功として扱い、最初の確定時刻を保持する。
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
	if _, ok := s.records[rec.ID]; ok {
		return errAssetAlreadyExists
	}
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
	if rec.ConfirmedAt != nil {
		return nil
	}
	rec.ConfirmedAt = &confirmedAt
	s.records[id] = rec
	return nil
}
