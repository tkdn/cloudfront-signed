//go:build wireinject

package main

import (
	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/wire"
)

func initializeRealRouteRegistrar(s3Client *s3.Client, urlSigner *sign.URLSigner, cfg config) routeRegistrar {
	wire.Build(
		wire.Bind(new(routeRegistrar), new(*realRouteRegistrar)),
		wire.Bind(new(s3PostPolicyPresigner), new(*realS3Adapter)),
		wire.Bind(new(s3ObjectHeadChecker), new(*realS3Adapter)),
		wire.Bind(new(cloudFrontSigner), new(*realCloudFrontSigner)),
		wire.Bind(new(assetStore), new(*memoryAssetStore)),
		wire.FieldsOf(new(config), "CloudFrontDomain", "Expires"),
		newRealS3Adapter,
		newRealCloudFrontSigner,
		newMemoryAssetStore,
		newRealRouteRegistrar,
	)
	return nil
}

func initializeMockRouteRegistrar(cfg config) routeRegistrar {
	wire.Build(
		wire.Bind(new(routeRegistrar), new(*mockRouteRegistrar)),
		wire.Bind(new(assetStore), new(*memoryAssetStore)),
		newMockS3Adapter,
		newMemoryAssetStore,
		newMockRouteRegistrar,
	)
	return nil
}
