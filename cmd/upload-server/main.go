package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	var routes routeRegistrar
	switch cfg.Mode {
	case modeMock:
		routes, err = newMockRoutes(cfg)
	default:
		routes, err = newRealRoutes(cfg)
	}
	if err != nil {
		return err
	}

	srv := newUploadServer(uploadServerConfig{
		StaticDir: "web",
		Routes:    routes,
	})

	fmt.Fprintf(os.Stderr, "listening on %s (mode=%s)\n", cfg.Addr, cfg.Mode)
	return http.ListenAndServe(cfg.Addr, srv.ServeMux())
}

// newRealRoutesは秘密鍵ロード・AWS SDK初期化を行った上でinitializeRealRouteRegistrarを呼ぶ。
func newRealRoutes(cfg config) (routeRegistrar, error) {
	f, err := os.Open(cfg.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("open private key: %w", err)
	}
	defer func() { _ = f.Close() }()

	signer, err := sign.LoadPEMPrivKeyPKCS8AsSigner(f)
	if err != nil {
		return nil, fmt.Errorf("load private key: %w", err)
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	s3Client := s3.NewFromConfig(awsCfg)
	urlSigner := sign.NewURLSigner(cfg.KeyPairID, signer)

	return initializeRealRouteRegistrar(s3Client, urlSigner, cfg), nil
}

// newMockRoutesはAWS呼び出しを一切行わず、ローカルファイルシステムを
// 実体とするmockRouteRegistrarを組み立てる。
func newMockRoutes(cfg config) (routeRegistrar, error) {
	if cfg.MockStorageDir == "" {
		dir, err := os.MkdirTemp("", "upload-server-mock-*")
		if err != nil {
			return nil, fmt.Errorf("create mock storage dir: %w", err)
		}
		cfg.MockStorageDir = dir
		fmt.Fprintf(os.Stderr, "UPLOAD_SERVER_MOCK_STORAGE_DIR not set, using temporary dir: %s\n", dir)
	}

	return initializeMockRouteRegistrar(cfg), nil
}
