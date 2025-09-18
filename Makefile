include make/license.mk

# Package related
BINARY_NAME=ib-kubernetes
PACKAGE=ib-kubernetes
ORG_PATH=github.com/Mellanox
REPO_PATH=$(ORG_PATH)/$(PACKAGE)
BUILDDIR=$(CURDIR)/build
BIN_DIR := $(PROJECT_DIR)/bin
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
BIN_DIR := $(PROJECT_DIR)/bin
PLUGINSSOURCEDIR=$(CURDIR)/pkg/sm/plugins
PLUGINSBUILDDIR=$(BUILDDIR)/plugins
GOFILES=$(shell find . -name *.go | grep -vE "(_test.go)")
PKGS = $$(go list ./... | grep -v "/test*" | grep -v ".*/mocks")

# Coverage
COVER_MODE = atomic
COVER_PROFILE = cover.out

# Version
VERSION?=master
DATE=`date -Iseconds`
COMMIT?=`git rev-parse --verify HEAD`
VERSION_LDFLAGS="-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"
PLUGIN_VERSION_LDFLAGS="-X $(REPO_PATH)/version=1.0"

# Docker
IMAGE_BUILDER?=@docker
DOCKERFILE?=$(CURDIR)/Dockerfile
TAG?=mellanox/ib-kubernetes
IMAGE_BUILD_OPTS?=
# Accept proxy settings for docker
# To pass proxy for Docker invoke it as 'make image HTTP_POXY=http://192.168.0.1:8080'
DOCKERARGS=
ifdef HTTP_PROXY
	DOCKERARGS += --build-arg http_proxy=$(HTTP_PROXY)
endif
ifdef HTTPS_PROXY
	DOCKERARGS += --build-arg https_proxy=$(HTTPS_PROXY)
endif
IMAGE_BUILD_OPTS += $(DOCKERARGS)

# Build Args
TARGETOS ?= $(shell go env GOOS)
TARGETARCH ?= $(shell go env GOARCH)
GO_BUILD_OPTS ?= CGO_ENABLED=1 GOOS=$(TARGETOS) GOARCH=$(TARGETARCH)
GO_LDFLAGS ?= $(VERSION_LDFLAGS)
GO_PLUGIN_LDFLAGS ?= $(PLUGIN_VERSION_LDFLAGS)
GO_TAGS ?= -tags no_openssl
GO_GCFLAGS ?=
export GOPATH?=$(shell go env GOPATH)
GOPROXY ?= $(shell go env GOPROXY)

# Go tools
GO      = go

TIMEOUT = 15
Q = $(if $(filter 1,$V),,@)

.PHONY: all
all: build plugins

$(BIN_DIR):; $(info Creating bin directory...)
	@mkdir -p $@

$(BUILDDIR): ; $(info Creating build directory...)
	@mkdir -p $@

$(PLUGINSBUILDDIR): ; $(info Creating plugins build directory...)
	@mkdir -p $@

# Tools
GOLANGCI_LINT = $(BIN_DIR)/golangci-lint
GOLANGCI_LINT_VERSION ?= v1.64.7
.PHONY: golangci-lint ## Download golangci-lint locally if necessary.
golangci-lint:
	@[ -f $(GOLANGCI_LINT) ] || { \
	set -e ;\
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(BIN_DIR) $(GOLANGCI_LINT_VERSION) ;\
	}

GOVERALLS := $(BIN_DIR)/goveralls
GOVERALLS_VERSION := latest
.PHONY: goveralls
goveralls: $(GOVERALLS) ## Download goveralls if necessary
$(GOVERALLS): | $(BIN_DIR); $(info  building goveralls...)
	$(call go-install-tool,$(GOVERALLS),github.com/mattn/goveralls@$(GOVERALLS_VERSION)

ENVTEST := $(BIN_DIR)/setup-envtest
ENVTEST_VERSION := latest
ENVTEST_K8S_VERSION := 1.30.0
.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest if necessary
$(ENVTEST): | $(BIN_DIR);$(info  building envtest...)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION))

GOCOVMERGE := $(BIN_DIR)/gocovmerge
GOCOVMERGE_VERSION := latest
.PHONY: gocovmerge
gocovmerge: $(GOCOVMERGE) ## Download gocovmerge if necessary
$(GOCOVMERGE): | $(BIN_DIR);$(info  building gocovmerge...)
	$(call go-install-tool,$(GOCOVMERGE),github.com/wadey/gocovmerge@$(GOCOVMERGE_VERSION))

GCOV2LCOV := $(BIN_DIR)/gcov2lcov
GCOV2LCOV_VERSION := v1.0.5
.PHONY: gcov2lcov
gcov2lcov: $(GCOV2LCOV) ## Download gcov2lcov if necessary
$(GCOV2LCOV): | $(BIN_DIR);$(info  building gcov2lcov...)
	$(call go-install-tool,$(GCOV2LCOV),github.com/jandelgado/gcov2lcov@$(GCOV2LCOV_VERSION))

# Build
build: $(BUILDDIR)/$(BINARY_NAME) ; $(info Building $(BINARY_NAME)...) ## Build executable file
	$(info Done!)

$(BUILDDIR)/$(BINARY_NAME): $(GOFILES) | $(BUILDDIR)
	$Q $(GO_BUILD_OPTS) $(GO) build -ldflags $(GO_LDFLAGS) -gcflags="$(GO_GCFLAGS)" -o $(BUILDDIR)/$(BINARY_NAME) $(GO_TAGS) -v $(CURDIR)/cmd/$(BINARY_NAME)/main.go

plugins: noop-plugin ufm-plugin  ; $(info Building plugins...) ## Build plugins
%-plugin: $(PLUGINSBUILDDIR)
	@echo Building $* plugin
	$Q $(GO_BUILD_OPTS) $(GO) build -ldflags $(GO_PLUGIN_LDFLAGS) -gcflags="$(GO_GCFLAGS)" -o $(PLUGINSBUILDDIR)/$*.so -buildmode=plugin $(GO_TAGS) -v $(REPO_PATH)/pkg/sm/plugins/$*
	@echo Done building $* plugin

plugins-coverage: noop-plugin-coverage ufm-plugin-coverage  ; $(info Building plugins with coverage...) ## Build plugins
%-plugin-coverage: $(PLUGINSBUILDDIR)
	@echo Building $* plugin
	$Q $(GO_BUILD_OPTS) $(GO) build -cover -covermode=$(COVER_MODE) -ldflags $(GO_PLUGIN_LDFLAGS) -gcflags="$(GO_GCFLAGS)" -o $(PLUGINSBUILDDIR)/$*.so -buildmode=plugin $(GO_TAGS) -v $(REPO_PATH)/pkg/sm/plugins/$*
	@echo Done building $* plugin

# Tests

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	$(GOLANGCI_LINT) run

TEST_TARGETS := test-bench test-short test-verbose test-race
.PHONY: $(TEST_TARGETS) test
test-bench:   ARGS=-run=__absolutelynothing__ -bench=. ## Run benchmarks
test-short:   ARGS=-short        ## Run only short tests
test-verbose: ARGS=-v            ## Run tests in verbose mode with coverage reporting
test-race:    GOFLAGS=-race         ## Run tests with race detector
$(TEST_TARGETS): NAME=$(MAKECMDGOALS:test-%=%)
$(TEST_TARGETS): test

test: | plugins; $(info  running $(NAME:%=% )tests...) @ ## Run tests
	$Q $(GO) test $(GOFLAGS) -timeout $(TIMEOUT)s $(ARGS) ./...

.PHONY: test-coverage
test-coverage: | plugins-coverage envtest gocovmerge gcov2lcov ## Run coverage tests
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(BIN_DIR) -p path)" go test -cover -covermode=$(COVER_MODE) -coverprofile=$(COVER_PROFILE) $(PKGS)

# Container image
.PHONY: image
image: ; $(info Building Docker image...)  ## Build conatiner image
	$(IMAGE_BUILDER) build -t $(TAG) -f $(DOCKERFILE)  $(CURDIR) --build-arg GOPROXY="$(GOPROXY)" $(IMAGE_BUILD_OPTS)

# Misc

.PHONY: clean
clean: ; $(info  Cleaning...)	 ## Cleanup everything
	@$(GO) clean -modcache
	@rm -rf $(BUILDDIR)
	@rm -rf $(BIN_DIR)
	@rm -rf  test

.PHONY: help
help: ## Show this message
	@grep -E '^[ a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

# go-get-tool will 'go get' any package $2 and install it to $1.
define go-install-tool
@[ -f $(1) ] || { \
set -e ;\
echo "Downloading $(2)" ;\
GOBIN=$(BIN_DIR) go install $(2) ;\
}
endef
