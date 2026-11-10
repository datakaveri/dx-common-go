// The apis module holds the platform's shared gRPC contracts: the .proto
// sources and the Go code generated from them. It is separate from
// dx-common-go so a caller depends on the contracts alone, and so contract
// versions (tags apis/vX.Y.Z) move independently of the framework.
module github.com/datakaveri/dx-common-go/apis

go 1.26.5

require (
	google.golang.org/grpc v1.82.1
	google.golang.org/protobuf v1.36.11
)

require (
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
)
