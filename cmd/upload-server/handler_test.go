package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeS3Presigner struct {
	url string
	err error
}

func (f *fakeS3Presigner) PresignPutObject(ctx context.Context, bucket, key, contentType string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.url, nil
}

type fakeCloudFrontSigner struct {
	url string
	err error
}

func (f *fakeCloudFrontSigner) SignDownloadURL(key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.url, nil
}

func newTestServer() *uploadServer {
	return newUploadServer(uploadServerConfig{
		UploadSecret:     "test-secret",
		S3Presigner:      &fakeS3Presigner{url: "https://bucket.s3.example.com/signed-put"},
		CloudFrontSigner: &fakeCloudFrontSigner{url: "https://cdn.example.com/signed-get"},
	})
}

func TestPresignUpload_Success(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-upload", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Key       string `json:"key"`
		UploadURL string `json:"uploadUrl"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.UploadURL != "https://bucket.s3.example.com/signed-put" {
		t.Fatalf("uploadUrl = %q, unexpected", got.UploadURL)
	}
	if !strings.HasPrefix(got.Key, "users/alice/") {
		t.Fatalf("key = %q, want prefix users/alice/", got.Key)
	}
}

func TestPresignUpload_WrongSecret(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-upload", body)
	req.Header.Set("X-Upload-Secret", "wrong-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPresignUpload_MissingSecret(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-upload", body)
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPresignUpload_InvalidContentType(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"userId":"alice","contentType":"application/octet-stream"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-upload", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPresignUpload_EmptyUserID(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"userId":"","contentType":"image/png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-upload", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPresignUpload_UserIDContainsSlash(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"userId":"alice/../bob","contentType":"image/png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-upload", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPresignUpload_PresignerError(t *testing.T) {
	srv := newUploadServer(uploadServerConfig{
		UploadSecret:     "test-secret",
		S3Presigner:      &fakeS3Presigner{err: errors.New("boom")},
		CloudFrontSigner: &fakeCloudFrontSigner{url: "https://cdn.example.com/signed-get"},
	})
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-upload", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestPresignDownload_Success(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"key":"users/alice/abc123.png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-download", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		DownloadURL string `json:"downloadUrl"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got.DownloadURL != "https://cdn.example.com/signed-get" {
		t.Fatalf("downloadUrl = %q, unexpected", got.DownloadURL)
	}
}

func TestPresignDownload_WrongSecret(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"key":"users/alice/abc123.png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-download", body)
	req.Header.Set("X-Upload-Secret", "wrong-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPresignDownload_EmptyKey(t *testing.T) {
	srv := newTestServer()
	body := strings.NewReader(`{"key":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-download", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPresignDownload_SignerError(t *testing.T) {
	srv := newUploadServer(uploadServerConfig{
		UploadSecret:     "test-secret",
		S3Presigner:      &fakeS3Presigner{url: "https://bucket.s3.example.com/signed-put"},
		CloudFrontSigner: &fakeCloudFrontSigner{err: errors.New("boom")},
	})
	body := strings.NewReader(`{"key":"users/alice/abc123.png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/presign-download", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
