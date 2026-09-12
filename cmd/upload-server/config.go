package main

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type serverMode string

const (
	modeReal serverMode = "real"
	modeMock serverMode = "mock"
)

type config struct {
	Mode             serverMode
	Addr             string
	Bucket           string
	CloudFrontDomain string
	KeyPairID        string
	PrivateKeyPath   string
	UploadSecret     string
	Expires          time.Duration
	MockStorageDir   string
}

const defaultExpires = 15 * time.Minute

func loadConfig() (config, error) {
	mode := serverMode(cmp.Or(os.Getenv("UPLOAD_SERVER_MODE"), string(modeReal)))
	if mode != modeReal && mode != modeMock {
		return config{}, fmt.Errorf("invalid UPLOAD_SERVER_MODE: %q (want %q or %q)", mode, modeReal, modeMock)
	}

	cfg := config{
		Mode:             mode,
		Addr:             cmp.Or(os.Getenv("UPLOAD_SERVER_ADDR"), ":8080"),
		Bucket:           os.Getenv("UPLOAD_SERVER_BUCKET"),
		CloudFrontDomain: os.Getenv("UPLOAD_SERVER_CLOUDFRONT_DOMAIN"),
		KeyPairID:        os.Getenv("UPLOAD_SERVER_KEY_PAIR_ID"),
		PrivateKeyPath:   os.Getenv("UPLOAD_SERVER_PRIVATE_KEY"),
		UploadSecret:     os.Getenv("UPLOAD_SERVER_UPLOAD_SECRET"),
		MockStorageDir:   os.Getenv("UPLOAD_SERVER_MOCK_STORAGE_DIR"),
	}

	var missing []string
	if cfg.Bucket == "" {
		missing = append(missing, "UPLOAD_SERVER_BUCKET")
	}
	if cfg.UploadSecret == "" {
		missing = append(missing, "UPLOAD_SERVER_UPLOAD_SECRET")
	}
	if mode == modeReal {
		if cfg.CloudFrontDomain == "" {
			missing = append(missing, "UPLOAD_SERVER_CLOUDFRONT_DOMAIN")
		}
		if cfg.KeyPairID == "" {
			missing = append(missing, "UPLOAD_SERVER_KEY_PAIR_ID")
		}
		if cfg.PrivateKeyPath == "" {
			missing = append(missing, "UPLOAD_SERVER_PRIVATE_KEY")
		}
	}
	if len(missing) > 0 {
		return config{}, fmt.Errorf("missing required environment variable(s): %s", strings.Join(missing, ", "))
	}

	expiresStr := os.Getenv("UPLOAD_SERVER_EXPIRES")
	if expiresStr == "" {
		cfg.Expires = defaultExpires
	} else {
		expires, err := time.ParseDuration(expiresStr)
		if err != nil {
			return config{}, fmt.Errorf("invalid UPLOAD_SERVER_EXPIRES: %w", err)
		}
		cfg.Expires = expires
	}
	if cfg.Expires <= 0 {
		return config{}, errors.New("UPLOAD_SERVER_EXPIRES must be a positive duration")
	}

	return cfg, nil
}
