# Regenerate the Go code for the shared contracts.
#
# Generated code is checked in, as in the parent module: callers build with
# `go build` alone.
#
# Requires: protoc, protoc-gen-go, protoc-gen-go-grpc
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
#
# `make check-java JAVA_PROTO=<path to dx-common>/src/main/proto` verifies the
# Java copies are byte-identical (see README.md).
.PHONY: proto check-java
proto:
	protoc --proto_path=proto \
	  --go_out=. --go_opt=module=github.com/datakaveri/dx-common-go/apis \
	  --go-grpc_out=. --go-grpc_opt=module=github.com/datakaveri/dx-common-go/apis \
	  $$(find proto -name '*.proto')

check-java:
	@test -n "$(JAVA_PROTO)" || { echo "set JAVA_PROTO=<dx-common>/src/main/proto"; exit 2; }
	diff -r proto/dx/apis $(JAVA_PROTO)/dx/apis && echo "Java copies are identical"
