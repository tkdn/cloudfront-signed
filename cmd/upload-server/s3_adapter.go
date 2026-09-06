package main

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type postPolicyForm struct {
	URL    string            `json:"url"`
	Fields map[string]string `json:"fields"`
}

type s3PostPolicyPresigner interface {
	PresignPostPolicy(ctx context.Context, bucket, key, contentType string, size int64, expires time.Duration) (postPolicyForm, error)
}

type s3ObjectHeadChecker interface {
	HeadObject(ctx context.Context, bucket, key string) error
}

type realS3Adapter struct {
	client        *s3.Client
	presignClient *s3.PresignClient
}

var _ s3PostPolicyPresigner = (*realS3Adapter)(nil)
var _ s3ObjectHeadChecker = (*realS3Adapter)(nil)

func newRealS3Adapter(client *s3.Client) *realS3Adapter {
	return &realS3Adapter{client: client, presignClient: s3.NewPresignClient(client)}
}

func (a *realS3Adapter) PresignPostPolicy(ctx context.Context, bucket, key, contentType string, size int64, expires time.Duration) (postPolicyForm, error) {
	req, err := a.presignClient.PresignPostObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}, func(o *s3.PresignPostOptions) {
		o.Expires = expires
		// GitHub/esa.ioの実測と同じく最小=最大に固定し、サイズ制約をS3の署名検証に転嫁する。
		// PutObjectInput.ContentTypeはPresignPostObjectでは無視されるため、Conditionsで明示する。
		o.Conditions = []any{
			[]any{"content-length-range", size, size},
			map[string]string{"Content-Type": contentType},
		}
	})
	if err != nil {
		return postPolicyForm{}, fmt.Errorf("presign post object: %w", err)
	}
	// SDKはConditionsで指定したContent-TypeをFieldsへ反映しないため、フォームフィールドとして明示的に追加する。
	req.Values["Content-Type"] = contentType
	return postPolicyForm{URL: req.URL, Fields: req.Values}, nil
}

func (a *realS3Adapter) HeadObject(ctx context.Context, bucket, key string) error {
	// サイズ・Content-Typeの一致はPresignPostPolicyのConditionsでS3の署名検証時に
	// 既に強制されているため、ここでは実体の存在確認のみ行う。
	_, err := a.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("head object: %w", err)
	}
	return nil
}
