package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func clearUploadServerEnv(t *testing.T) {
	t.Helper()
	vars := []string{
		"UPLOAD_SERVER_ADDR",
		"UPLOAD_SERVER_BUCKET",
		"UPLOAD_SERVER_CLOUDFRONT_DOMAIN",
		"UPLOAD_SERVER_KEY_PAIR_ID",
		"UPLOAD_SERVER_PRIVATE_KEY",
		"UPLOAD_SERVER_UPLOAD_SECRET",
		"UPLOAD_SERVER_EXPIRES",
		"UPLOAD_SERVER_MODE",
		"UPLOAD_SERVER_MOCK_STORAGE_DIR",
	}
	for _, v := range vars {
		t.Setenv(v, "")
		_ = os.Unsetenv(v)
	}
}

func setAllRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("UPLOAD_SERVER_BUCKET", "test-bucket")
	t.Setenv("UPLOAD_SERVER_CLOUDFRONT_DOMAIN", "test.cloudfront.net")
	t.Setenv("UPLOAD_SERVER_KEY_PAIR_ID", "K123")
	t.Setenv("UPLOAD_SERVER_PRIVATE_KEY", "keys/private_key.pem")
	t.Setenv("UPLOAD_SERVER_UPLOAD_SECRET", "test-secret")
}

func TestLoadConfig_Success(t *testing.T) {
	clearUploadServerEnv(t)
	setAllRequiredEnv(t)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Fatalf("Addr = %q, want default \":8080\"", cfg.Addr)
	}
	if cfg.Bucket != "test-bucket" {
		t.Fatalf("Bucket = %q, want %q", cfg.Bucket, "test-bucket")
	}
	if cfg.CloudFrontDomain != "test.cloudfront.net" {
		t.Fatalf("CloudFrontDomain = %q, want %q", cfg.CloudFrontDomain, "test.cloudfront.net")
	}
	if cfg.KeyPairID != "K123" {
		t.Fatalf("KeyPairID = %q, want %q", cfg.KeyPairID, "K123")
	}
	if cfg.PrivateKeyPath != "keys/private_key.pem" {
		t.Fatalf("PrivateKeyPath = %q, want %q", cfg.PrivateKeyPath, "keys/private_key.pem")
	}
	if cfg.UploadSecret != "test-secret" {
		t.Fatalf("UploadSecret = %q, want %q", cfg.UploadSecret, "test-secret")
	}
	if cfg.Expires != 15*time.Minute {
		t.Fatalf("Expires = %v, want default 15m", cfg.Expires)
	}
	if cfg.Mode != modeReal {
		t.Fatalf("Mode = %q, want default %q", cfg.Mode, modeReal)
	}
}

func TestLoadConfig_CustomAddrAndExpires(t *testing.T) {
	clearUploadServerEnv(t)
	setAllRequiredEnv(t)
	t.Setenv("UPLOAD_SERVER_ADDR", ":9090")
	t.Setenv("UPLOAD_SERVER_EXPIRES", "30m")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Addr != ":9090" {
		t.Fatalf("Addr = %q, want %q", cfg.Addr, ":9090")
	}
	if cfg.Expires != 30*time.Minute {
		t.Fatalf("Expires = %v, want 30m", cfg.Expires)
	}
}

func TestLoadConfig_MissingAllRequired(t *testing.T) {
	clearUploadServerEnv(t)

	_, err := loadConfig()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "missing required environment variable(s)") {
		t.Fatalf("err = %v, want message containing %q", err, "missing required environment variable(s)")
	}
	for _, want := range []string{
		"UPLOAD_SERVER_BUCKET",
		"UPLOAD_SERVER_CLOUDFRONT_DOMAIN",
		"UPLOAD_SERVER_KEY_PAIR_ID",
		"UPLOAD_SERVER_PRIVATE_KEY",
		"UPLOAD_SERVER_UPLOAD_SECRET",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to mention %q", err, want)
		}
	}
}

func TestLoadConfig_MissingOneRequired(t *testing.T) {
	clearUploadServerEnv(t)
	setAllRequiredEnv(t)
	_ = os.Unsetenv("UPLOAD_SERVER_BUCKET")

	_, err := loadConfig()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "UPLOAD_SERVER_BUCKET") {
		t.Fatalf("err = %v, want it to mention UPLOAD_SERVER_BUCKET", err)
	}
	if strings.Contains(err.Error(), "UPLOAD_SERVER_UPLOAD_SECRET") {
		t.Fatalf("err = %v, should not mention UPLOAD_SERVER_UPLOAD_SECRET (it was set)", err)
	}
}

func TestLoadConfig_InvalidExpires(t *testing.T) {
	clearUploadServerEnv(t)
	setAllRequiredEnv(t)
	t.Setenv("UPLOAD_SERVER_EXPIRES", "not-a-duration")

	_, err := loadConfig()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid UPLOAD_SERVER_EXPIRES") {
		t.Fatalf("err = %v, want message containing %q", err, "invalid UPLOAD_SERVER_EXPIRES")
	}
}

func TestLoadConfig_NonPositiveExpires(t *testing.T) {
	clearUploadServerEnv(t)
	setAllRequiredEnv(t)
	t.Setenv("UPLOAD_SERVER_EXPIRES", "0s")

	_, err := loadConfig()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "positive duration") {
		t.Fatalf("err = %v, want message containing %q", err, "positive duration")
	}
}

func TestLoadConfig_ModeMock_SkipsCloudFrontAndKeyRequirements(t *testing.T) {
	clearUploadServerEnv(t)
	t.Setenv("UPLOAD_SERVER_MODE", "mock")
	t.Setenv("UPLOAD_SERVER_BUCKET", "test-bucket")
	t.Setenv("UPLOAD_SERVER_UPLOAD_SECRET", "test-secret")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Mode != modeMock {
		t.Fatalf("Mode = %q, want %q", cfg.Mode, modeMock)
	}
}

func TestLoadConfig_ModeMock_StillRequiresBucketAndSecret(t *testing.T) {
	clearUploadServerEnv(t)
	t.Setenv("UPLOAD_SERVER_MODE", "mock")

	_, err := loadConfig()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	for _, want := range []string{"UPLOAD_SERVER_BUCKET", "UPLOAD_SERVER_UPLOAD_SECRET"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to mention %q", err, want)
		}
	}
	for _, notWant := range []string{"UPLOAD_SERVER_CLOUDFRONT_DOMAIN", "UPLOAD_SERVER_KEY_PAIR_ID", "UPLOAD_SERVER_PRIVATE_KEY"} {
		if strings.Contains(err.Error(), notWant) {
			t.Fatalf("err = %v, should not mention %q in mock mode", err, notWant)
		}
	}
}

func TestLoadConfig_InvalidMode(t *testing.T) {
	clearUploadServerEnv(t)
	setAllRequiredEnv(t)
	t.Setenv("UPLOAD_SERVER_MODE", "bogus")

	_, err := loadConfig()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid UPLOAD_SERVER_MODE") {
		t.Fatalf("err = %v, want message containing %q", err, "invalid UPLOAD_SERVER_MODE")
	}
}

func TestLoadConfig_ModeReal_RequiresCloudFrontAndKeyFields(t *testing.T) {
	clearUploadServerEnv(t)
	t.Setenv("UPLOAD_SERVER_BUCKET", "test-bucket")
	t.Setenv("UPLOAD_SERVER_UPLOAD_SECRET", "test-secret")

	_, err := loadConfig()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	for _, want := range []string{"UPLOAD_SERVER_CLOUDFRONT_DOMAIN", "UPLOAD_SERVER_KEY_PAIR_ID", "UPLOAD_SERVER_PRIVATE_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to mention %q", err, want)
		}
	}
}
