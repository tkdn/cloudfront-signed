package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"
)

type cloudFrontSigner interface {
	SignDownloadURL(key string) (url string, err error)
}

// realRouteRegistrarはAWS(S3/CloudFront)を実際に呼び出す本番用のルート実装。
type realRouteRegistrar struct {
	cfg              config
	store            assetStore
	s3Presigner      s3PostPolicyPresigner
	s3HeadChecker    s3ObjectHeadChecker
	cloudFrontSigner cloudFrontSigner
}

var _ routeRegistrar = (*realRouteRegistrar)(nil)

func newRealRouteRegistrar(
	cfg config,
	store assetStore,
	presigner s3PostPolicyPresigner,
	headChecker s3ObjectHeadChecker,
	signer cloudFrontSigner,
) *realRouteRegistrar {
	return &realRouteRegistrar{
		cfg:              cfg,
		store:            store,
		s3Presigner:      presigner,
		s3HeadChecker:    headChecker,
		cloudFrontSigner: signer,
	}
}

func (r *realRouteRegistrar) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/upload/policies", r.handleUploadPolicies)
	mux.HandleFunc("PATCH /api/upload/assets/{id...}", r.handleConfirmAsset)
	mux.HandleFunc("GET /assets/{id...}", r.handleGetAsset)
}

func (r *realRouteRegistrar) checkSecret(w http.ResponseWriter, req *http.Request) bool {
	got := req.Header.Get("X-Upload-Secret")
	if subtle.ConstantTimeCompare([]byte(got), []byte(r.cfg.UploadSecret)) != 1 {
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

func (r *realRouteRegistrar) handleUploadPolicies(w http.ResponseWriter, req *http.Request) {
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

	form, err := r.s3Presigner.PresignPostPolicy(req.Context(), r.cfg.Bucket, id, body.ContentType, body.Size, r.cfg.Expires)
	if err != nil {
		http.Error(w, "presign post policy: "+err.Error(), http.StatusInternalServerError)
		return
	}

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

type confirmAssetRequest struct {
	ConfirmToken string `json:"confirmToken"`
}

func (r *realRouteRegistrar) handleConfirmAsset(w http.ResponseWriter, req *http.Request) {
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

	if err := r.s3HeadChecker.HeadObject(req.Context(), r.cfg.Bucket, rec.ID); err != nil {
		http.Error(w, "object not found in S3: "+err.Error(), http.StatusNotFound)
		return
	}

	if err := r.store.Confirm(req.Context(), id, time.Now()); err != nil {
		http.Error(w, "confirm asset record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// 検証目的のため認証・認可を行わずidの推測困難性のみに依存している。実運用では
// ここでリクエスト元の認証（セッション等）とアセットへのアクセス認可を必須にすること。
func (r *realRouteRegistrar) handleGetAsset(w http.ResponseWriter, req *http.Request) {
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

	downloadURL, err := r.cloudFrontSigner.SignDownloadURL(rec.ID)
	if err != nil {
		http.Error(w, "sign download url: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, req, downloadURL, http.StatusFound)
}
