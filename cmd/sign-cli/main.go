package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	url := flag.String("url", "", "URL to sign (required)")
	resource := flag.String("resource", "", "Custom Policy Resource field; defaults to -url")
	keyPairID := flag.String("key-pair-id", "", "CloudFront public key ID (required)")
	privateKeyPath := flag.String("private-key", "", "path to PKCS8 PEM private key (required)")
	expires := flag.Duration("expires", time.Hour, "signature validity duration from now")
	flag.Parse()

	if *url == "" || *keyPairID == "" || *privateKeyPath == "" {
		flag.Usage()
		return fmt.Errorf("missing required flag(s)")
	}
	if *expires <= 0 {
		return fmt.Errorf("-expires must be a positive duration")
	}

	res := *resource
	if res == "" {
		res = *url
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

	policy := &sign.Policy{
		Statements: []sign.Statement{
			{
				Resource: res,
				Condition: sign.Condition{
					DateLessThan: sign.NewAWSEpochTime(time.Now().Add(*expires)),
				},
			},
		},
	}

	urlSigner := sign.NewURLSigner(*keyPairID, signer)
	signedURL, err := urlSigner.SignWithPolicy(*url, policy)
	if err != nil {
		return fmt.Errorf("sign url: %w", err)
	}

	fmt.Println(signedURL)
	return nil
}
