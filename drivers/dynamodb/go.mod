module proctor/drivers/dynamodb

go 1.27.1

require (
	github.com/aws/aws-sdk-go-v2 v1.48.0
	github.com/aws/aws-sdk-go-v2/credentials v1.21.0
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.74.0
	github.com/aws/smithy-go v1.28.9
	github.com/bezmoradi/proctor/protocol v0.0.0
)

require (
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.12 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.25 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.13.12 // indirect
)

replace github.com/bezmoradi/proctor/protocol => ../../protocol
