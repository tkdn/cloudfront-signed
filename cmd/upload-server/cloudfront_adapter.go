// cmd/upload-server/cloudfront_adapter.go
package main

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
)

type realCloudFrontSigner struct {
	domain    string
	urlSigner *sign.URLSigner
	expires   time.Duration
}

func newRealCloudFrontSigner(domain string, signer *sign.URLSigner, expires time.Duration) *realCloudFrontSigner {
	return &realCloudFrontSigner{domain: domain, urlSigner: signer, expires: expires}
}

func (c *realCloudFrontSigner) SignDownloadURL(key string) (string, error) {
	url := fmt.Sprintf("https://%s/%s", c.domain, key)
	policy := &sign.Policy{
		Statements: []sign.Statement{
			{
				Resource: url,
				Condition: sign.Condition{
					DateLessThan: sign.NewAWSEpochTime(time.Now().Add(c.expires)),
				},
			},
		},
	}
	signedURL, err := c.urlSigner.SignWithPolicy(url, policy)
	if err != nil {
		return "", fmt.Errorf("sign with policy: %w", err)
	}
	return signedURL, nil
}
