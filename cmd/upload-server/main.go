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

	f, err := os.Open(cfg.PrivateKeyPath)
	if err != nil {
		return fmt.Errorf("open private key: %w", err)
	}
	defer func() { _ = f.Close() }()

	signer, err := sign.LoadPEMPrivKeyPKCS8AsSigner(f)
	if err != nil {
		return fmt.Errorf("load private key: %w", err)
	}

	ctx := context.Background()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}
	s3Client := s3.NewFromConfig(awsCfg)
	urlSigner := sign.NewURLSigner(cfg.KeyPairID, signer)

	deps := initializeUploadServerDeps(s3Client, urlSigner, cfg.CloudFrontDomain, cfg.Expires)

	srv := newUploadServer(uploadServerConfig{
		Bucket:           cfg.Bucket,
		UploadSecret:     cfg.UploadSecret,
		PostExpires:      cfg.Expires,
		ConfirmExpires:   cfg.Expires,
		StaticDir:        "web",
		Store:            deps.Store,
		S3Presigner:      deps.S3Presigner,
		S3HeadChecker:    deps.S3HeadChecker,
		CloudFrontSigner: deps.CloudFrontSigner,
	})

	fmt.Fprintf(os.Stderr, "listening on %s\n", cfg.Addr)
	return http.ListenAndServe(cfg.Addr, srv.ServeMux())
}
