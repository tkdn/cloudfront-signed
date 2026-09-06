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

type uploadServerConfig struct {
	Bucket           string
	UploadSecret     string
	PostExpires      time.Duration
	ConfirmExpires   time.Duration
	Store            assetStore
	S3Presigner      s3PostPolicyPresigner
	S3HeadChecker    s3ObjectHeadChecker
	CloudFrontSigner cloudFrontSigner
}

type uploadServer struct {
	cfg uploadServerConfig
	mux *http.ServeMux
}

func newUploadServer(cfg uploadServerConfig) *uploadServer {
	s := &uploadServer{cfg: cfg, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /api/upload/policies", s.handleUploadPolicies)
	s.mux.HandleFunc("PATCH /api/upload/assets/{id...}", s.handleConfirmAsset)
	s.mux.HandleFunc("GET /assets/{id...}", s.handleGetAsset)
	return s
}

func (s *uploadServer) ServeMux() *http.ServeMux {
	return s.mux
}

func (s *uploadServer) checkSecret(w http.ResponseWriter, r *http.Request) bool {
	got := r.Header.Get("X-Upload-Secret")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.UploadSecret)) != 1 {
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

func (s *uploadServer) handleUploadPolicies(w http.ResponseWriter, r *http.Request) {
	if !s.checkSecret(w, r) {
		return
	}

	var req uploadPoliciesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Size <= 0 {
		http.Error(w, "size must be a positive integer", http.StatusBadRequest)
		return
	}

	id, err := newObjectKey(req.UserID, req.ContentType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	form, err := s.cfg.S3Presigner.PresignPostPolicy(r.Context(), s.cfg.Bucket, id, req.ContentType, req.Size, s.cfg.PostExpires)
	if err != nil {
		http.Error(w, "presign post policy: "+err.Error(), http.StatusInternalServerError)
		return
	}

	now := time.Now()
	rec := assetRecord{
		ID:          id,
		UserID:      req.UserID,
		ContentType: req.ContentType,
		Size:        req.Size,
		CreatedAt:   now,
	}
	if err := s.cfg.Store.Create(r.Context(), rec); err != nil {
		http.Error(w, "create asset record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	token := newConfirmToken(id, now.Add(s.cfg.ConfirmExpires))

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

func (s *uploadServer) handleConfirmAsset(w http.ResponseWriter, r *http.Request) {
	if !s.checkSecret(w, r) {
		return
	}

	id := r.PathValue("id")

	var req confirmAssetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	if !verifyConfirmToken(id, req.ConfirmToken) {
		http.Error(w, "invalid or expired confirmToken", http.StatusUnauthorized)
		return
	}

	rec, err := s.cfg.Store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, errAssetNotFound) {
			http.Error(w, "asset not found", http.StatusNotFound)
			return
		}
		http.Error(w, "get asset record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := s.cfg.S3HeadChecker.HeadObject(r.Context(), s.cfg.Bucket, rec.ID); err != nil {
		http.Error(w, "object not found in S3: "+err.Error(), http.StatusNotFound)
		return
	}

	if err := s.cfg.Store.Confirm(r.Context(), id, time.Now()); err != nil {
		http.Error(w, "confirm asset record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *uploadServer) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	rec, err := s.cfg.Store.Get(r.Context(), id)
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

	downloadURL, err := s.cfg.CloudFrontSigner.SignDownloadURL(rec.ID)
	if err != nil {
		http.Error(w, "sign download url: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, downloadURL, http.StatusFound)
}
