module github.com/Mellanox/ib-kubernetes

go 1.13

require (
	github.com/caarlos0/env/v6 v6.2.1
	github.com/googleapis/gnostic v0.4.0 // indirect
	github.com/hashicorp/golang-lru v0.5.4 // indirect
	github.com/imdario/mergo v0.3.8 // indirect
	github.com/json-iterator/go v1.1.9 // indirect
	github.com/k8snetworkplumbingwg/network-attachment-definition-client v0.0.0-20200626054723-37f83d1996bc
	github.com/kr/pretty v0.2.0 // indirect
	github.com/onsi/ginkgo v1.12.0
	github.com/onsi/gomega v1.9.0
	github.com/pkg/errors v0.9.1 // indirect
	github.com/rs/zerolog v1.18.0
	github.com/stretchr/testify v1.5.1
	go.uber.org/multierr v1.5.0 // indirect
	golang.org/x/oauth2 v0.0.0-20200107190931-bf48bf16ab8d // indirect
	golang.org/x/text v0.3.3 // indirect
	golang.org/x/xerrors v0.0.0-20191204190536-9bdfabe68543 // indirect
	k8s.io/api v0.18.3
	k8s.io/apimachinery v0.18.3
	k8s.io/client-go v0.18.3
	sigs.k8s.io/controller-runtime v0.5.2
)

replace (
	k8s.io/client-go => k8s.io/client-go v0.18.3
	k8s.io/code-generator => k8s.io/code-generator v0.18.3
)
