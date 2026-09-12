package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// mockRouteRegistrarはAWSを一切呼び出さず、ローカルファイルシステムのみで
// 完結するルート実装。GET /assets/{id}は実ファイルを直接配信する
// （real版のようなCloudFront署名URLへのリダイレクトは行わない）。
//
// アップロード受け口(/api/upload/objects/{id})はこのサーバー自身が
// 提供するため、form.URLは常に相対パスを返す。絶対URL（ホスト名や
// スキームを含む値）にすると、ブラウザからは別オリジン扱いになり
// CORSエラーの原因になる（例: localhostで開いたページに対して
// 127.0.0.1宛の絶対URLを返すと失敗する）。
type mockRouteRegistrar struct {
	cfg     config
	store   assetStore
	storage *mockS3Adapter
}

var _ routeRegistrar = (*mockRouteRegistrar)(nil)

func newMockRouteRegistrar(cfg config, store assetStore, storage *mockS3Adapter) *mockRouteRegistrar {
	return &mockRouteRegistrar{
		cfg:     cfg,
		store:   store,
		storage: storage,
	}
}

func (r *mockRouteRegistrar) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/upload/policies", r.handleUploadPolicies)
	mux.HandleFunc("POST /api/upload/objects/{id...}", r.handleUploadObject)
	mux.HandleFunc("PATCH /api/upload/assets/{id...}", r.handleConfirmAsset)
	mux.HandleFunc("GET /assets/{id...}", r.handleGetAsset)
}

func (r *mockRouteRegistrar) checkSecret(w http.ResponseWriter, req *http.Request) bool {
	got := req.Header.Get("X-Upload-Secret")
	if subtle.ConstantTimeCompare([]byte(got), []byte(r.cfg.UploadSecret)) != 1 {
		http.Error(w, "invalid or missing X-Upload-Secret header", http.StatusUnauthorized)
		return false
	}
	return true
}

func (r *mockRouteRegistrar) handleUploadPolicies(w http.ResponseWriter, req *http.Request) {
	if !r.checkSecret(w, req) {
		return
	}

	var body uploadPoliciesRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if body.Size <= 0 {
		http.Error(w, "size must be a positive integer", http.StatusBadRequest)
		return
	}

	id, err := newObjectKey(body.UserID, body.ContentType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	form, err := r.storage.PresignPostPolicy(req.Context(), r.cfg.Bucket, id, body.ContentType, body.Size, r.cfg.Expires)
	if err != nil {
		http.Error(w, "presign post policy: "+err.Error(), http.StatusInternalServerError)
		return
	}
	form.URL = "/api/upload/objects/" + id

	now := time.Now()
	rec := assetRecord{
		ID:          id,
		UserID:      body.UserID,
		ContentType: body.ContentType,
		Size:        body.Size,
		CreatedAt:   now,
	}
	if err := r.store.Create(req.Context(), rec); err != nil {
		if errors.Is(err, errAssetAlreadyExists) {
			http.Error(w, "asset id collision, retry", http.StatusConflict)
			return
		}
		http.Error(w, "create asset record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	token := newConfirmToken(id, now.Add(r.cfg.Expires))

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

// handleUploadObjectはPresignPostPolicyが発行したURLへのmultipart POSTを
// 受け付け、ファイル本体をローカルストレージへ保存する。real環境のS3への
// 直接POSTを模した動作。
//
// real環境ではS3のPOST Policy条件（content-length-range・Content-Type）が
// 申告値との一致を強制するため、mockでも同じ契約をhandleUploadPolicies時に
// 記録したassetRecordとの突き合わせで再現する。この検証を省くと、mockで
// 確認した挙動がreal環境の挙動を代表しなくなる。
func (r *mockRouteRegistrar) handleUploadObject(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}

	rec, err := r.store.Get(req.Context(), id)
	if err != nil {
		if errors.Is(err, errAssetNotFound) {
			http.Error(w, "asset not found", http.StatusNotFound)
			return
		}
		http.Error(w, "get asset record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := req.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "parse multipart form: "+err.Error(), http.StatusBadRequest)
		return
	}

	if gotContentType := req.FormValue("Content-Type"); gotContentType != rec.ContentType {
		http.Error(w, fmt.Sprintf("content-type mismatch: got %q, want %q", gotContentType, rec.ContentType), http.StatusBadRequest)
		return
	}

	file, _, err := req.FormFile("file")
	if err != nil {
		http.Error(w, "missing file field: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "read uploaded file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if int64(len(data)) != rec.Size {
		http.Error(w, fmt.Sprintf("size mismatch: got %d bytes, want %d", len(data), rec.Size), http.StatusBadRequest)
		return
	}

	if err := r.storage.Save(id, data); err != nil {
		http.Error(w, "save uploaded file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (r *mockRouteRegistrar) handleConfirmAsset(w http.ResponseWriter, req *http.Request) {
	if !r.checkSecret(w, req) {
		return
	}

	id := req.PathValue("id")

	var body confirmAssetRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if !verifyConfirmToken(id, body.ConfirmToken) {
		http.Error(w, "invalid or expired confirmToken", http.StatusUnauthorized)
		return
	}

	rec, err := r.store.Get(req.Context(), id)
	if err != nil {
		if errors.Is(err, errAssetNotFound) {
			http.Error(w, "asset not found", http.StatusNotFound)
			return
		}
		http.Error(w, "get asset record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := r.storage.HeadObject(req.Context(), r.cfg.Bucket, rec.ID); err != nil {
		http.Error(w, "object not found in mock storage: "+err.Error(), http.StatusNotFound)
		return
	}

	if err := r.store.Confirm(req.Context(), id, time.Now()); err != nil {
		http.Error(w, "confirm asset record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// handleGetAssetはconfirmed済みレコードの実ファイルを直接配信する
// （SignDownloadURLに相当する経路は無く、cloudFrontSignerを経由しない）。
func (r *mockRouteRegistrar) handleGetAsset(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")

	rec, err := r.store.Get(req.Context(), id)
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

	f, err := r.storage.Open(rec.ID)
	if err != nil {
		http.Error(w, "asset not found in mock storage: "+err.Error(), http.StatusNotFound)
		return
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "stat asset: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, req, rec.ID, stat.ModTime(), f)
}
