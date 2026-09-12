package main

import (
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newMockTestServer(t *testing.T) *uploadServer {
	t.Helper()
	cfg := config{
		Bucket:         "mock-bucket",
		UploadSecret:   "test-secret",
		Expires:        15 * time.Minute,
		MockStorageDir: t.TempDir(),
	}
	storage := newMockS3Adapter(cfg)
	routes := newMockRouteRegistrar(cfg, newMemoryAssetStore(), storage)
	return newUploadServer(uploadServerConfig{Routes: routes})
}

func buildMultipartBody(t *testing.T, fields map[string]string, fileContent []byte) (body *strings.Reader, contentType string) {
	t.Helper()
	var buf strings.Builder
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	}
	fw, err := w.CreateFormFile("file", "asset.png")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write(fileContent); err != nil {
		t.Fatalf("write file content: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return strings.NewReader(buf.String()), w.FormDataContentType()
}

func TestMockUploadFlow_EndToEnd(t *testing.T) {
	srv := newMockTestServer(t)

	// 1. ポリシー発行
	policyBody := strings.NewReader(`{"userId":"alice","contentType":"image/png","size":4}`)
	policyReq := httptest.NewRequest(http.MethodPost, "/api/upload/policies", policyBody)
	policyReq.Header.Set("X-Upload-Secret", "test-secret")
	policyRec := httptest.NewRecorder()
	srv.ServeMux().ServeHTTP(policyRec, policyReq)
	if policyRec.Code != http.StatusOK {
		t.Fatalf("policies: status = %d, want 200; body=%s", policyRec.Code, policyRec.Body.String())
	}
	var policy struct {
		ID           string `json:"id"`
		ConfirmToken string `json:"confirmToken"`
		Form         struct {
			URL    string            `json:"url"`
			Fields map[string]string `json:"fields"`
		} `json:"form"`
	}
	if err := json.Unmarshal(policyRec.Body.Bytes(), &policy); err != nil {
		t.Fatalf("unmarshal policy response: %v", err)
	}
	// form.URLは相対パスであること（絶対URLだとブラウザから見て別オリジン
	// 扱いになりCORSエラーの原因になるため、常にリクエスト元と同一オリジンで
	// 解決される相対パスを返す必要がある）。
	if strings.HasPrefix(policy.Form.URL, "http://") || strings.HasPrefix(policy.Form.URL, "https://") {
		t.Fatalf("form.url = %q, want a relative path (no scheme/host) to avoid cross-origin requests", policy.Form.URL)
	}
	if !strings.HasPrefix(policy.Form.URL, "/api/upload/objects/") {
		t.Fatalf("form.url = %q, want prefix /api/upload/objects/", policy.Form.URL)
	}

	// 2. multipart POSTでアップロード
	fileContent := []byte("data")
	body, contentType := buildMultipartBody(t, policy.Form.Fields, fileContent)
	uploadReq := httptest.NewRequest(http.MethodPost, policy.Form.URL, body)
	uploadReq.Header.Set("Content-Type", contentType)
	uploadRec := httptest.NewRecorder()
	srv.ServeMux().ServeHTTP(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusNoContent {
		t.Fatalf("upload: status = %d, want 204; body=%s", uploadRec.Code, uploadRec.Body.String())
	}

	// 3. confirm
	confirmBody := strings.NewReader(`{"confirmToken":"` + policy.ConfirmToken + `"}`)
	confirmReq := httptest.NewRequest(http.MethodPatch, "/api/upload/assets/"+policy.ID, confirmBody)
	confirmReq.Header.Set("X-Upload-Secret", "test-secret")
	confirmRec := httptest.NewRecorder()
	srv.ServeMux().ServeHTTP(confirmRec, confirmReq)
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("confirm: status = %d, want 200; body=%s", confirmRec.Code, confirmRec.Body.String())
	}

	// 4. 閲覧（直接配信）
	getReq := httptest.NewRequest(http.MethodGet, "/assets/"+policy.ID, nil)
	getRec := httptest.NewRecorder()
	srv.ServeMux().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get asset: status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}
	got, err := io.ReadAll(getRec.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if string(got) != string(fileContent) {
		t.Fatalf("downloaded content = %q, want %q", got, fileContent)
	}
}

func TestMockHandleUploadObject_MissingFileField(t *testing.T) {
	srv := newMockTestServer(t)
	_, _, fields, url := issueMockPolicy(t, srv, "image/png", 4)

	var buf strings.Builder
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
	}
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(buf.String()))
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestMockHandleUploadObject_UnknownID(t *testing.T) {
	srv := newMockTestServer(t)

	var buf strings.Builder
	w := multipart.NewWriter(&buf)
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/upload/objects/users/alice/does-not-exist.png", strings.NewReader(buf.String()))
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func issueMockPolicy(t *testing.T, srv *uploadServer, contentType string, size int64) (id, confirmToken string, fields map[string]string, url string) {
	t.Helper()
	policyBody := strings.NewReader(`{"userId":"alice","contentType":"` + contentType + `","size":` + strconv.FormatInt(size, 10) + `}`)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/policies", policyBody)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()
	srv.ServeMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("policies: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var policy struct {
		ID           string `json:"id"`
		ConfirmToken string `json:"confirmToken"`
		Form         struct {
			URL    string            `json:"url"`
			Fields map[string]string `json:"fields"`
		} `json:"form"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &policy); err != nil {
		t.Fatalf("unmarshal policy response: %v", err)
	}
	return policy.ID, policy.ConfirmToken, policy.Form.Fields, policy.Form.URL
}

func TestMockHandleUploadObject_SizeMismatch(t *testing.T) {
	srv := newMockTestServer(t)
	_, _, fields, url := issueMockPolicy(t, srv, "image/png", 100)

	// 申告サイズ(100)と実際のペイロード("data" = 4バイト)が一致しない。
	body, contentType := buildMultipartBody(t, fields, []byte("data"))
	req := httptest.NewRequest(http.MethodPost, url, body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestMockHandleUploadObject_ContentTypeMismatch(t *testing.T) {
	srv := newMockTestServer(t)
	fileContent := []byte("data")
	_, _, fields, url := issueMockPolicy(t, srv, "image/png", int64(len(fileContent)))

	// フォームのContent-Typeフィールドをポリシー発行時と異なる値に差し替える。
	fields["Content-Type"] = "application/octet-stream"
	body, contentType := buildMultipartBody(t, fields, fileContent)
	req := httptest.NewRequest(http.MethodPost, url, body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestMockHandleGetAsset_UnknownID(t *testing.T) {
	srv := newMockTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/assets/users/alice/does-not-exist.png", nil)
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestMockHandleGetAsset_UnconfirmedReturnsNotFound(t *testing.T) {
	srv := newMockTestServer(t)

	policyBody := strings.NewReader(`{"userId":"alice","contentType":"image/png","size":4}`)
	policyReq := httptest.NewRequest(http.MethodPost, "/api/upload/policies", policyBody)
	policyReq.Header.Set("X-Upload-Secret", "test-secret")
	policyRec := httptest.NewRecorder()
	srv.ServeMux().ServeHTTP(policyRec, policyReq)
	var policy struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(policyRec.Body.Bytes(), &policy); err != nil {
		t.Fatalf("unmarshal policy response: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/assets/"+policy.ID, nil)
	rec := httptest.NewRecorder()
	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
