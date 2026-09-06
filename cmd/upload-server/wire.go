//go:build wireinject

package main

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/wire"
)

type serverDeps struct {
	S3Presigner      s3PostPolicyPresigner
	S3HeadChecker    s3ObjectHeadChecker
	CloudFrontSigner cloudFrontSigner
	Store            assetStore
}

func initializeUploadServerDeps(s3Client *s3.Client, urlSigner *sign.URLSigner, cloudfrontDomain string, expires time.Duration) serverDeps {
	wire.Build(
		wire.Struct(new(serverDeps), "*"),
		newRealS3Adapter,
		wire.Bind(new(s3PostPolicyPresigner), new(*realS3Adapter)),
		wire.Bind(new(s3ObjectHeadChecker), new(*realS3Adapter)),
		newRealCloudFrontSigner,
		wire.Bind(new(cloudFrontSigner), new(*realCloudFrontSigner)),
		newMemoryAssetStore,
		wire.Bind(new(assetStore), new(*memoryAssetStore)),
	)
	return serverDeps{}
}
