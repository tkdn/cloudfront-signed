package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakePostPolicyPresigner struct {
	form postPolicyForm
	err  error
}

var _ s3PostPolicyPresigner = (*fakePostPolicyPresigner)(nil)

func (f *fakePostPolicyPresigner) PresignPostPolicy(_ context.Context, _, _, _ string, _ int64, _ time.Duration) (postPolicyForm, error) {
	if f.err != nil {
		return postPolicyForm{}, f.err
	}
	return f.form, nil
}

type fakeHeadChecker struct {
	err error
}

var _ s3ObjectHeadChecker = (*fakeHeadChecker)(nil)

func (f *fakeHeadChecker) HeadObject(_ context.Context, _, _ string) error {
	return f.err
}

type fakeCloudFrontSigner struct {
	url string
	err error
}

var _ cloudFrontSigner = (*fakeCloudFrontSigner)(nil)

func (f *fakeCloudFrontSigner) SignDownloadURL(_ string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.url, nil
}

func newTestServer() (*uploadServer, *memoryAssetStore) {
	store := newMemoryAssetStore()
	routes := newRealRouteRegistrar(
		config{Bucket: "test-bucket", UploadSecret: "test-secret", Expires: 15 * time.Minute},
		store,
		&fakePostPolicyPresigner{form: postPolicyForm{
			URL:    "https://bucket.s3.example.com/",
			Fields: map[string]string{"key": "placeholder"},
		}},
		&fakeHeadChecker{},
		&fakeCloudFrontSigner{url: "https://cdn.example.com/signed-get"},
	)
	srv := newUploadServer(uploadServerConfig{Routes: routes})
	return srv, store
}

func TestUploadPolicies_Success(t *testing.T) {
	srv, _ := newTestServer()
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png","size":22945}`)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/policies", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		ID           string `json:"id"`
		ConfirmToken string `json:"confirmToken"`
		Form         struct {
			URL    string            `json:"url"`
			Fields map[string]string `json:"fields"`
		} `json:"form"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !strings.HasPrefix(got.ID, "users/alice/") {
		t.Fatalf("id = %q, want prefix users/alice/", got.ID)
	}
	if got.ConfirmToken == "" {
		t.Fatalf("confirmToken must not be empty")
	}
	if got.Form.URL != "https://bucket.s3.example.com/" {
		t.Fatalf("form.url = %q, unexpected", got.Form.URL)
	}
	if !verifyConfirmToken(got.ID, got.ConfirmToken) {
		t.Fatalf("issued confirmToken does not verify against issued id")
	}
}

func TestUploadPolicies_WrongSecret(t *testing.T) {
	srv, _ := newTestServer()
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png","size":22945}`)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/policies", body)
	req.Header.Set("X-Upload-Secret", "wrong-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestUploadPolicies_InvalidContentType(t *testing.T) {
	srv, _ := newTestServer()
	body := strings.NewReader(`{"userId":"alice","contentType":"application/octet-stream","size":100}`)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/policies", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestUploadPolicies_EmptyUserID(t *testing.T) {
	srv, _ := newTestServer()
	body := strings.NewReader(`{"userId":"","contentType":"image/png","size":100}`)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/policies", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestUploadPolicies_NonPositiveSize(t *testing.T) {
	srv, _ := newTestServer()
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png","size":0}`)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/policies", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestUploadPolicies_MethodNotAllowed(t *testing.T) {
	srv, _ := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/api/upload/policies", nil)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestUploadPolicies_MethodNotAllowed_WithStaticDir(t *testing.T) {
	routes := newRealRouteRegistrar(
		config{UploadSecret: "test-secret"},
		newMemoryAssetStore(),
		&fakePostPolicyPresigner{},
		&fakeHeadChecker{},
		&fakeCloudFrontSigner{},
	)
	srv := newUploadServer(uploadServerConfig{
		StaticDir: ".", // 実在するディレクトリなら何でもよい（cmd/upload-serverのソースディレクトリ自体を使う）
		Routes:    routes,
	})
	req := httptest.NewRequest(http.MethodGet, "/api/upload/policies", nil)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	// StaticDirが設定されている場合、method-awareルートにマッチしないリクエストは
	// FileServerにフォールバックする（405ではなくFileServer側のステータスになる）。
	if rec.Code == http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want non-405 (FileServer fallback) when StaticDir is set", rec.Code)
	}
}

func TestUploadPolicies_PresignerError(t *testing.T) {
	store := newMemoryAssetStore()
	routes := newRealRouteRegistrar(
		config{Bucket: "test-bucket", UploadSecret: "test-secret", Expires: 15 * time.Minute},
		store,
		&fakePostPolicyPresigner{err: errors.New("boom")},
		&fakeHeadChecker{},
		&fakeCloudFrontSigner{url: "https://cdn.example.com/signed-get"},
	)
	srv := newUploadServer(uploadServerConfig{Routes: routes})
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png","size":100}`)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/policies", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func createTestAsset(t *testing.T, srv *uploadServer) (id, confirmToken string) {
	t.Helper()
	body := strings.NewReader(`{"userId":"alice","contentType":"image/png","size":100}`)
	req := httptest.NewRequest(http.MethodPost, "/api/upload/policies", body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()
	srv.ServeMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		ID           string `json:"id"`
		ConfirmToken string `json:"confirmToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("setup: unmarshal response: %v", err)
	}
	return got.ID, got.ConfirmToken
}

func TestConfirmAsset_Success(t *testing.T) {
	srv, store := newTestServer()
	id, token := createTestAsset(t, srv)

	body := strings.NewReader(`{"confirmToken":"` + token + `"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/upload/assets/"+id, body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("store.Get: unexpected error: %v", err)
	}
	if got.ConfirmedAt == nil {
		t.Fatalf("ConfirmedAt = nil, want non-nil after confirm")
	}
}

func TestConfirmAsset_WrongSecret(t *testing.T) {
	srv, _ := newTestServer()
	id, token := createTestAsset(t, srv)

	body := strings.NewReader(`{"confirmToken":"` + token + `"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/upload/assets/"+id, body)
	req.Header.Set("X-Upload-Secret", "wrong-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestConfirmAsset_WrongToken(t *testing.T) {
	srv, _ := newTestServer()
	id, _ := createTestAsset(t, srv)

	body := strings.NewReader(`{"confirmToken":"wrong-token"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/upload/assets/"+id, body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestConfirmAsset_UnknownID(t *testing.T) {
	srv, _ := newTestServer()
	unknownID := "users/alice/does-not-exist.png"
	token := newConfirmToken(unknownID, time.Now().Add(15*time.Minute))

	body := strings.NewReader(`{"confirmToken":"` + token + `"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/upload/assets/"+unknownID, body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestConfirmAsset_HeadObjectFails(t *testing.T) {
	store := newMemoryAssetStore()
	routes := newRealRouteRegistrar(
		config{Bucket: "test-bucket", UploadSecret: "test-secret", Expires: 15 * time.Minute},
		store,
		&fakePostPolicyPresigner{form: postPolicyForm{
			URL:    "https://bucket.s3.example.com/",
			Fields: map[string]string{"key": "placeholder"},
		}},
		&fakeHeadChecker{err: errors.New("not found in S3")},
		&fakeCloudFrontSigner{url: "https://cdn.example.com/signed-get"},
	)
	srv := newUploadServer(uploadServerConfig{Routes: routes})
	id, token := createTestAsset(t, srv)

	body := strings.NewReader(`{"confirmToken":"` + token + `"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/upload/assets/"+id, body)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	got, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("store.Get: unexpected error: %v", err)
	}
	if got.ConfirmedAt != nil {
		t.Fatalf("ConfirmedAt = %v, want nil after failed HeadObject", *got.ConfirmedAt)
	}
}

func TestConfirmAsset_MethodNotAllowed(t *testing.T) {
	srv, _ := newTestServer()
	id, _ := createTestAsset(t, srv)

	req := httptest.NewRequest(http.MethodPost, "/api/upload/assets/"+id, nil)
	req.Header.Set("X-Upload-Secret", "test-secret")
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestGetAsset_RedirectsToSignedURL(t *testing.T) {
	srv, _ := newTestServer()
	id, token := createTestAsset(t, srv)
	confirmReq := httptest.NewRequest(http.MethodPatch, "/api/upload/assets/"+id, strings.NewReader(`{"confirmToken":"`+token+`"}`))
	confirmReq.Header.Set("X-Upload-Secret", "test-secret")
	confirmRec := httptest.NewRecorder()
	srv.ServeMux().ServeHTTP(confirmRec, confirmReq)
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("setup confirm: status = %d, want 200", confirmRec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/assets/"+id, nil)
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "https://cdn.example.com/signed-get" {
		t.Fatalf("Location = %q, unexpected", loc)
	}
}

func TestGetAsset_UnconfirmedReturnsNotFound(t *testing.T) {
	srv, _ := newTestServer()
	id, _ := createTestAsset(t, srv)

	req := httptest.NewRequest(http.MethodGet, "/assets/"+id, nil)
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestGetAsset_UnknownID(t *testing.T) {
	srv, _ := newTestServer()

	req := httptest.NewRequest(http.MethodGet, "/assets/users/alice/does-not-exist.png", nil)
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestGetAsset_SignerError(t *testing.T) {
	store := newMemoryAssetStore()
	routes := newRealRouteRegistrar(
		config{Bucket: "test-bucket", UploadSecret: "test-secret", Expires: 15 * time.Minute},
		store,
		&fakePostPolicyPresigner{form: postPolicyForm{
			URL:    "https://bucket.s3.example.com/",
			Fields: map[string]string{"key": "placeholder"},
		}},
		&fakeHeadChecker{},
		&fakeCloudFrontSigner{err: errors.New("boom")},
	)
	srv := newUploadServer(uploadServerConfig{Routes: routes})
	id, token := createTestAsset(t, srv)
	confirmReq := httptest.NewRequest(http.MethodPatch, "/api/upload/assets/"+id, strings.NewReader(`{"confirmToken":"`+token+`"}`))
	confirmReq.Header.Set("X-Upload-Secret", "test-secret")
	confirmRec := httptest.NewRecorder()
	srv.ServeMux().ServeHTTP(confirmRec, confirmReq)
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("setup confirm: status = %d, want 200", confirmRec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/assets/"+id, nil)
	rec := httptest.NewRecorder()

	srv.ServeMux().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
