.PHONY: proto build test docker run clean tidy tools

GOPATH := $(shell go env GOPATH)
PROTOC_GEN_GO := $(GOPATH)/bin/protoc-gen-go
PROTOC_GEN_GO_GRPC := $(GOPATH)/bin/protoc-gen-go-grpc
GEN_DIR := gen/go

# Install protoc-gen-go and protoc-gen-go-grpc if not present.
$(PROTOC_GEN_GO):
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest

$(PROTOC_GEN_GO_GRPC):
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

tools: $(PROTOC_GEN_GO) $(PROTOC_GEN_GO_GRPC)

# Generate Go bindings from proto/. Requires protoc on PATH; the Go plugins
# are auto-installed under $(GOPATH)/bin if missing.
proto: tools
	mkdir -p $(GEN_DIR)
	PATH=$(GOPATH)/bin:$$PATH protoc \
		--go_out=$(GEN_DIR) --go_opt=paths=source_relative \
		--go-grpc_out=$(GEN_DIR) --go-grpc_opt=paths=source_relative,require_unimplemented_servers=false \
		-I proto \
		proto/bandwidth/v1/quota.proto

build:
	go build -o bin/bandwidth-quota ./cmd/bandwidth-quota

test:
	go test ./...

# Run the server against a local Redis (default 127.0.0.1:6379).
run: build
	./bin/bandwidth-quota --listen=:9300 --redis-addr=127.0.0.1:6379

docker:
	docker build -t bandwidth-quota:dev .

tidy:
	go mod tidy

clean:
	rm -rf bin gen
