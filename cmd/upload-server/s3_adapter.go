package main

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type realS3Presigner struct {
	client  *s3.PresignClient
	expires time.Duration
}

var _ s3Presigner = (*realS3Presigner)(nil)

func newRealS3Presigner(client *s3.Client, expires time.Duration) *realS3Presigner {
	return &realS3Presigner{client: s3.NewPresignClient(client), expires: expires}
}

func (p *realS3Presigner) PresignPutObject(ctx context.Context, bucket, key, contentType string) (string, error) {
	req, err := p.client.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}, s3.WithPresignExpires(p.expires))
	if err != nil {
		return "", fmt.Errorf("presign put object: %w", err)
	}
	return req.URL, nil
}
