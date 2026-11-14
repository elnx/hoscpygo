VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BIN := hoscpy

.PHONY: all build run test vet fmt proto dist clean linux darwin windows \
	native-src native-deps native-server native-asset native-loader dist-native

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BIN) ./cmd/hoscpy

run:
	CGO_ENABLED=0 go run ./cmd/hoscpy -no-open

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

proto:
	protoc --go_out=. --go_opt=module=hoscpygo --go-grpc_out=. --go-grpc_opt=module=hoscpygo proto/scrcpy.proto

dist: linux darwin windows

# Native targets are optional and POSIX-only (Linux, macOS, WSL). The OpenHarmony
# third_party components are pinned as submodules under NATIVE_SRC; ordinary Go
# users do not need them because server_static.z.so is embedded.
#
#   make native-src    # initialize/update the pinned OpenHarmony submodules
#   make native-deps   # build absl/protobuf/gRPC/... + server (incremental)
#   make native-server # rebuild the server against existing native deps
#   make native-asset  # strip + copy the server into internal/assets/device/
#   make native-loader # rebuild the standalone root loader asset
#   make dist-native   # one-shot: submodules + deps + asset + all Go dist binaries
#
# native-deps/native-server are intentionally phony: CMake/Ninja does the real
# incremental dependency tracking, so C++ and gRPC-submodule changes are
# recompiled by make dist-native without a separate Make timestamp database.
OHOS ?= native/server/ohos-src
NATIVE_BUILD ?= native/server/build
NATIVE_ASSET ?= internal/assets/device/server_static.z.so
NATIVE_STRIP ?= $(if $(OHOS_NDK),$(OHOS_NDK)/llvm/bin/llvm-strip,llvm-strip)
LOADER_ASSET ?= internal/assets/device/hoscpy_loader
NATIVE_SUBMODULES := $(shell git config -f .gitmodules --get-regexp '^submodule\..*\.path$$' 2>/dev/null | cut -d' ' -f2)

native-src:
	git submodule update --init -- $(NATIVE_SUBMODULES)

native-deps: native-src
	OHOS="$(OHOS)" ./native/server/build-deps.sh

native-server: native-src
	BUILD=$(NATIVE_BUILD) ./native/server/build.sh

native-asset: native-server
	$(NATIVE_STRIP) --strip-unneeded $(NATIVE_BUILD)/hoscpy_server.z.so
	cp $(NATIVE_BUILD)/hoscpy_server.z.so $(NATIVE_ASSET)

# The standalone loader is tiny and depends only on the NDK, not on the
# OpenHarmony third_party submodules. Its output is committed because ordinary
# builds embed it.
native-loader:
	@test -n "$(OHOS_NDK)" || { echo "set OHOS_NDK to the OpenHarmony native SDK" >&2; exit 2; }
	"$(OHOS_NDK)/llvm/bin/aarch64-unknown-linux-ohos-clang" \
		--target=aarch64-linux-ohos --sysroot="$(OHOS_NDK)/sysroot" \
		-O2 -s -o $(LOADER_ASSET) native/loader/loader.c -ldl
	file $(LOADER_ASSET)

dist-native: native-deps
	$(MAKE) native-asset
	$(MAKE) dist

linux:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BIN)-linux-amd64 ./cmd/hoscpy

darwin:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BIN)-darwin-amd64 ./cmd/hoscpy
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BIN)-darwin-arm64 ./cmd/hoscpy

windows:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BIN)-windows-amd64.exe ./cmd/hoscpy

clean:
	rm -rf bin dist native/server/build
