VERSION ?= 3.0.0
ARCH ?= $(shell go env GOARCH)
OS ?= $(shell go env GOOS)
RPM_ARCH ?= $(ARCH)
ifeq ($(ARCH),amd64)
	RPM_ARCH := x86_64
else ifeq ($(ARCH),arm64)
	RPM_ARCH := aarch64
endif
BINARY ?= dist/shareto

.PHONY: all build test clean deb rpm tarball package package-all macos windows print-version

all: build

print-version:
	@echo $(VERSION)

build:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=$(OS) GOARCH=$(ARCH) GOTOOLCHAIN=local go build \
		-trimpath \
		-ldflags="-s -w -X main.Version=$(VERSION)" \
		-o $(BINARY) ./cmd/shareto

test:
	GOTOOLCHAIN=local go test -v -race ./...

deb: build
	VERSION=$(VERSION) ARCH=$(ARCH) nfpm pkg --config packaging/nfpm.yaml --packager deb --target dist/shareto_$(VERSION)_$(ARCH).deb

rpm: build
	VERSION=$(VERSION) ARCH=$(RPM_ARCH) nfpm pkg --config packaging/nfpm.yaml --packager rpm --target dist/shareto-$(VERSION)-1.$(RPM_ARCH).rpm

tarball: build
	@mkdir -p dist/tar-staging/shareto
	@cp $(BINARY) dist/tar-staging/shareto/
	@cp packaging/shareto.service dist/tar-staging/shareto/
	@cp README.md dist/tar-staging/shareto/
	@cp LICENSE dist/tar-staging/shareto/
	@tar -czf dist/shareto_$(VERSION)_linux_$(ARCH).tar.gz -C dist/tar-staging shareto
	@rm -rf dist/tar-staging
	@echo "created tarball: dist/shareto_$(VERSION)_linux_$(ARCH).tar.gz"

ifeq ($(OS),darwin)
package: macos
else
package: deb rpm tarball
endif

package-all:
	@for arch in amd64 arm64; do \
		echo "===> Building packages for $$arch..."; \
		$(MAKE) package OS=linux ARCH=$$arch; \
	done

macos:
	$(MAKE) build OS=darwin ARCH=arm64 BINARY=dist/macos/arm64/shareto
	$(MAKE) build OS=darwin ARCH=amd64 BINARY=dist/macos/amd64/shareto
	VERSION=$(VERSION) bash packaging/build-macos.sh

windows:
	@mkdir -p dist/shareto-windows-amd64 dist/shareto-windows-arm64
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 GOTOOLCHAIN=local go build \
		-trimpath \
		-ldflags="-s -w -X main.Version=$(VERSION)" \
		-o dist/shareto-windows-amd64/shareto.exe ./cmd/shareto
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 GOTOOLCHAIN=local go build \
		-trimpath \
		-ldflags="-s -w -X main.Version=$(VERSION)" \
		-o dist/shareto-windows-arm64/shareto.exe ./cmd/shareto
	VERSION=$(VERSION) bash packaging/build-windows.sh

clean:
	rm -rf dist/
