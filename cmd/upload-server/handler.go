package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
)

type s3Presigner interface {
	PresignPutObject(ctx context.Context, bucket, key, contentType string) (url string, err error)
}

type cloudFrontSigner interface {
	SignDownloadURL(key string) (url string, err error)
}

type uploadServerConfig struct {
	Bucket           string
	UploadSecret     string
	S3Presigner      s3Presigner
	CloudFrontSigner cloudFrontSigner
}

type uploadServer struct {
	cfg uploadServerConfig
	mux *http.ServeMux
}

func newUploadServer(cfg uploadServerConfig) *uploadServer {
	s := &uploadServer{cfg: cfg, mux: http.NewServeMux()}
	s.mux.HandleFunc("/api/presign-upload", s.handlePresignUpload)
	s.mux.HandleFunc("/api/presign-download", s.handlePresignDownload)
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

type presignUploadRequest struct {
	UserID      string `json:"userId"`
	ContentType string `json:"contentType"`
}

type presignUploadResponse struct {
	Key       string `json:"key"`
	UploadURL string `json:"uploadUrl"`
}

func (s *uploadServer) handlePresignUpload(w http.ResponseWriter, r *http.Request) {
	if !s.checkSecret(w, r) {
		return
	}

	var req presignUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	key, err := newObjectKey(req.UserID, req.ContentType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	uploadURL, err := s.cfg.S3Presigner.PresignPutObject(r.Context(), s.cfg.Bucket, key, req.ContentType)
	if err != nil {
		http.Error(w, "presign upload url: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(presignUploadResponse{Key: key, UploadURL: uploadURL}); err != nil {
		log.Printf("encode presign-upload response: %v", err)
	}
}

type presignDownloadRequest struct {
	Key string `json:"key"`
}

type presignDownloadResponse struct {
	DownloadURL string `json:"downloadUrl"`
}

func (s *uploadServer) handlePresignDownload(w http.ResponseWriter, r *http.Request) {
	if !s.checkSecret(w, r) {
		return
	}

	var req presignDownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Key == "" {
		http.Error(w, "key must not be empty", http.StatusBadRequest)
		return
	}

	downloadURL, err := s.cfg.CloudFrontSigner.SignDownloadURL(req.Key)
	if err != nil {
		http.Error(w, "sign download url: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(presignDownloadResponse{DownloadURL: downloadURL}); err != nil {
		log.Printf("encode presign-download response: %v", err)
	}
}
