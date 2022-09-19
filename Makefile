# Package related
BINARY_NAME=ib-kubernetes
PACKAGE=ib-kubernetes
ORG_PATH=github.com/Mellanox
REPO_PATH=$(ORG_PATH)/$(PACKAGE)
GOPATH=$(CURDIR)/.gopath
GOBIN =$(CURDIR)/bin
BUILDDIR=$(CURDIR)/build
COVERAGE_DIR := $(BUILDDIR)/coverage
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
BIN_DIR := $(PROJECT_DIR)/bin
PLUGINSSOURCEDIR=$(CURDIR)/pkg/sm/plugins
PLUGINSBUILDDIR=$(BUILDDIR)/plugins
GOFILES=$(shell find . -name *.go | grep -vE "(\/vendor\/)|(_test.go)")
PKGS := $(shell cd $(PROJECT_DIR) && go list ./... | grep -v mocks)
TESTPKGS := $(shell go list -f '{{ if or .TestGoFiles .XTestGoFiles }}{{ .ImportPath }}{{ end }}' $(PKGS))

export GOPATH
export GOBIN
export CGO_ENABLED=1

# Version
VERSION?=master
DATE=`date -Iseconds`
COMMIT?=`git rev-parse --verify HEAD`
LDFLAGS="-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

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

# Go tools
GO      = go
GOLANGCI_LINT_VERSION = v1.23.8
GOLANGCI_LINT = $(GOBIN)/golangci-lint
TIMEOUT = 15
Q = $(if $(filter 1,$V),,@)

ENVTEST := $(BIN_DIR)/setup-envtest
GOCOVMERGE := $(BIN_DIR)/gocovmerge
GCOV2LCOV := $(BIN_DIR)/gcov2lcov

.PHONY: all
all: build plugins

$(GOBIN):
	@mkdir -p $@

$(BUILDDIR): ; $(info Creating build directory...)
	@mkdir -p $@

$(PLUGINSBUILDDIR): ; $(info Creating plugins build directory...)
	@mkdir -p $@

build: $(BUILDDIR)/$(BINARY_NAME) ; $(info Building $(BINARY_NAME)...) ## Build executable file
	$(info Done!)

$(BUILDDIR)/$(BINARY_NAME): $(GOFILES) | $(BUILDDIR)
	@cd cmd/$(BINARY_NAME) && $(GO) build -o $(BUILDDIR)/$(BINARY_NAME) -tags no_openssl -ldflags $(LDFLAGS) -v

# Tools

$(GOLANGCI_LINT): ; $(info  building golangci-lint...)
	$Q curl -sfL https://install.goreleaser.com/github.com/golangci/golangci-lint.sh | sh -s -- -b $(GOBIN) $(GOLANGCI_LINT_VERSION)

GOVERALLS = $(GOBIN)/goveralls
$(GOBIN)/goveralls: ; $(info  building goveralls...)
	$Q go get github.com/mattn/goveralls

# Tests

.PHONY: lint
lint: | $(GOLANGCI_LINT) ; $(info  running golangci-lint...) @ ## Run golangci-lint
	$Q ret=0 \
		test -z "$$($(GOLANGCI_LINT) run --timeout 10m0s | tee ./lint.out)" || ret=1 ; \
	cat ./lint.out; rm -f ./lint.out; \
	exit $$ret

plugins: noop-plugin ufm-plugin  ; $(info Building plugins...) ## Build plugins

%-plugin: $(PLUGINSBUILDDIR)
	@echo Building $* plugin
	$Q $(GO) build $(GOFLAGS) -ldflags "-X $(REPO_PATH)/version=1.0" -o $(PLUGINSBUILDDIR)/$*.so -buildmode=plugin $(REPO_PATH)/pkg/sm/plugins/$*
	@echo Done building $* plugin

TEST_TARGETS := test-bench test-short test-verbose test-race
.PHONY: $(TEST_TARGETS) test
test-bench:   ARGS=-run=__absolutelynothing__ -bench=. ## Run benchmarks
test-short:   ARGS=-short        ## Run only short tests
test-verbose: ARGS=-v            ## Run tests in verbose mode with coverage reporting
test-race:    GOFLAGS=-race         ## Run tests with race detector
$(TEST_TARGETS): NAME=$(MAKECMDGOALS:test-%=%)
$(TEST_TARGETS): test

test: | plugins; $(info  running $(NAME:%=% )tests...) @ ## Run tests
	$Q $(GO) test -timeout $(TIMEOUT)s $(ARGS)  ./...

COVERAGE_MODE = count
.PHONY: test-coverage test-coverage-tools
test-coverage: | envtest gocovmerge gcov2lcov ## Run coverage tests
	mkdir -p $(BUILDDIR)/coverage/pkgs
	for pkg in $(TESTPKGS); do \
		KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" go test \
		-covermode=atomic \
		-coverprofile="$(COVERAGE_DIR)/pkgs/`echo $$pkg | tr "/" "-"`.cover" $$pkg ;\
	done
	$(GOCOVMERGE) $(COVERAGE_DIR)/pkgs/*.cover > $(COVERAGE_DIR)/profile.out
	$(GCOV2LCOV) -infile $(COVERAGE_DIR)/profile.out -outfile $(COVERAGE_DIR)/lcov.info

.PHONY: envtest
envtest: ## Download envtest if necessary
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest@latest)

.PHONY: gocovmerge
gocovmerge: ## Download gocovmerge if necessary
	$(call go-install-tool,$(GOCOVMERGE),github.com/wadey/gocovmerge@latest)

.PHONY: gcov2lcov
gcov2lcov: ## Download gcov2lcov if necessary
	$(call go-install-tool,$(GCOV2LCOV),github.com/jandelgado/gcov2lcov@v1.0.5)

# Container image
.PHONY: image
image: ; $(info Building Docker image...)  ## Build conatiner image
	$(IMAGE_BUILDER) build -t $(TAG) -f $(DOCKERFILE)  $(CURDIR) $(IMAGE_BUILD_OPTS)


# Misc

.PHONY: clean
clean: ; $(info  Cleaning...)	 ## Cleanup everything
	@$(GO) clean -modcache
	@rm -rf $(GOPATH)
	@rm -rf $(BUILDDIR)
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
GOBIN=$(PROJECT_DIR)/bin go install $(2) ;\
}
endef