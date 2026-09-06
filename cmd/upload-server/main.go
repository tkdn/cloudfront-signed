package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

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
	addr := flag.String("addr", ":8080", "HTTP server listen address")
	bucket := flag.String("bucket", "", "S3 bucket name for uploads (required)")
	cloudfrontDomain := flag.String("cloudfront-domain", "", "CloudFront domain for signed download URLs (required)")
	keyPairID := flag.String("key-pair-id", "", "CloudFront public key ID (required)")
	privateKeyPath := flag.String("private-key", "", "path to PKCS8 PEM private key (required)")
	uploadSecret := flag.String("upload-secret", "", "shared secret required in X-Upload-Secret header (required)")
	expires := flag.Duration("expires", 15*time.Minute, "signed URL / confirm token validity duration from now")
	flag.Parse()

	if *bucket == "" || *cloudfrontDomain == "" || *keyPairID == "" || *privateKeyPath == "" || *uploadSecret == "" {
		flag.Usage()
		return fmt.Errorf("missing required flag(s)")
	}
	if *expires <= 0 {
		return fmt.Errorf("-expires must be a positive duration")
	}

	f, err := os.Open(*privateKeyPath)
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
	s3Adapter := newRealS3Adapter(s3Client)

	urlSigner := sign.NewURLSigner(*keyPairID, signer)

	srv := newUploadServer(uploadServerConfig{
		Bucket:           *bucket,
		UploadSecret:     *uploadSecret,
		PostExpires:      *expires,
		ConfirmExpires:   *expires,
		StaticDir:        "web",
		Store:            newMemoryAssetStore(),
		S3Presigner:      s3Adapter,
		S3HeadChecker:    s3Adapter,
		CloudFrontSigner: newRealCloudFrontSigner(*cloudfrontDomain, urlSigner, *expires),
	})

	fmt.Fprintf(os.Stderr, "listening on %s\n", *addr)
	return http.ListenAndServe(*addr, srv.ServeMux())
}
