VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BIN := hoscpygo

.PHONY: all build run test vet fmt proto dist clean linux darwin windows \
	native-deps native-server native-asset dist-native

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BIN) ./cmd/hoscpygo

run:
	CGO_ENABLED=0 go run ./cmd/hoscpygo -no-open

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

proto:
	protoc --go_out=. --go_opt=module=hoscpygo --go-grpc_out=. --go-grpc_opt=module=hoscpygo proto/scrcpy.proto

dist: linux darwin windows

# Native targets are optional and POSIX-only (Linux, macOS, WSL). They need a
# full OpenHarmony source tree plus the OHOS NDK; ordinary Go users do not need
# them because server_static.z.so is embedded.
#
#   make native-deps   # build absl/protobuf/gRPC/... + server (slow)
#   make native-server # rebuild the server against existing native deps
#   make native-asset  # strip + copy the server into internal/assets/device/
#   make dist-native   # one-shot: deps + asset + all Go dist binaries
NATIVE_BUILD ?= native/server/build
NATIVE_ASSET ?= internal/assets/device/server_static.z.so
NATIVE_STRIP ?= $(if $(OHOS_NDK),$(OHOS_NDK)/llvm/bin/llvm-strip,llvm-strip)

native-deps:
	./native/server/build-deps.sh

native-server:
	BUILD=$(NATIVE_BUILD) ./native/server/build.sh

native-asset: native-server
	$(NATIVE_STRIP) --strip-unneeded $(NATIVE_BUILD)/hoscpy_server.z.so
	cp $(NATIVE_BUILD)/hoscpy_server.z.so $(NATIVE_ASSET)

dist-native: native-deps
	$(MAKE) native-asset
	$(MAKE) dist

linux:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BIN)-linux-amd64 ./cmd/hoscpygo

darwin:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BIN)-darwin-amd64 ./cmd/hoscpygo
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BIN)-darwin-arm64 ./cmd/hoscpygo

windows:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BIN)-windows-amd64.exe ./cmd/hoscpygo

clean:
	rm -rf bin dist native/server/build
