# kernel-style V=1 build verbosity
ifeq ("$(origin V)", "command line")
       BUILD_VERBOSE = $(V)
endif

ifeq ($(BUILD_VERBOSE),1)
       Q =
else
       Q = @
endif

VERSION = $(shell git describe --dirty --tags --always)
REPO = github.com/operator-framework/helm-app-operator-kit/helm-app-operator
BUILD_PATH = $(REPO)/cmd/manager
PKGS = $(shell go list ./... | grep -v /vendor/)

export CGO_ENABLED:=0

all: format test build/operator-sdk

format:
	$(Q)go fmt $(PKGS)

dep:
	$(Q)dep ensure -v

clean:
	$(Q)rm -rf release build/_output

.PHONY: all test format dep clean

install:
	./build/build.sh

release_x86_64 := \
	release/helm-app-operator-$(VERSION)-x86_64-linux-gnu \
	release/helm-app-operator-$(VERSION)-x86_64-apple-darwin

release: clean $(release_x86_64)

release/helm-app-operator-%-x86_64-linux-gnu: GOARGS = GOOS=linux GOARCH=amd64
release/helm-app-operator-%-x86_64-apple-darwin: GOARGS = GOOS=darwin GOARCH=amd64

release/%:
	$(Q)$(GOARGS) go build -o $@ $(BUILD_PATH)
	
DEFAULT_KEY = $(shell gpgconf --list-options gpg \
								| awk -F: '$$1 == "default-key" { gsub(/"/,""); print toupper($$10)}')
GIT_KEY = $(shell git config --get user.signingkey | awk '{ print toupper($$0) }')
release/%.asc:
ifeq ("$(DEFAULT_KEY)","$(GIT_KEY)")
	$(Q)gpg --output $@ --detach-sig build/$*
	$(Q)gpg --verify $@ build/$*
else
	@echo "git and/or gpg are not configured to have default signing key ${DEFAULT_KEY}"
	@exit 1
endif

.PHONY: install release_x86_64 release

test: dep test/sanity test/unit install test/e2e

test/ci-helm: test/e2e/helm

test/sanity:
	./hack/tests/sanity-check.sh

test/unit:
	./hack/tests/unit.sh

test/e2e: test/e2e/helm

test/e2e/helm:
	./hack/tests/e2e-helm.sh

.PHONY: test test/sanity test/unit test/e2e test/e2e/helm test/ci-helm