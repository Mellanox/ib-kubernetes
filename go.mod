module github.com/Mellanox/ib-kubernetes

go 1.13

require (
	github.com/caarlos0/env v3.5.0+incompatible
	github.com/containernetworking/cni v0.7.1
	github.com/davecgh/go-spew v1.1.1
	github.com/fsnotify/fsnotify v1.4.7
	github.com/go-logr/logr v0.1.0
	github.com/gogo/protobuf v1.3.1
	github.com/golang/glog v0.0.0-20160126235308-23def4e6c14b
	github.com/golang/protobuf v1.3.3
	github.com/google/go-cmp v0.4.0
	github.com/google/gofuzz v1.1.0
	github.com/googleapis/gnostic v0.4.0
	github.com/hashicorp/golang-lru v0.5.4
	github.com/hpcloud/tail v1.0.0
	github.com/imdario/mergo v0.3.8
	github.com/json-iterator/go v1.1.9
	github.com/k8snetworkplumbingwg/network-attachment-definition-client v0.0.0-20200127152046-0ee521d56061
	//golang.org/x/time v0.0.0-20200124190032-861946025e34
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd
	github.com/modern-go/reflect2 v1.0.1
	github.com/onsi/ginkgo v1.12.0
	github.com/onsi/gomega v1.9.0
	github.com/pkg/errors v0.9.1
	github.com/pmezard/go-difflib v1.0.0
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/objx v0.2.0
	github.com/stretchr/testify v1.4.0
	//	github.com/golang/crypto v0.0.0-20200128174031-69ecbb4d6d5d
	golang.org/x/crypto v0.0.0-20200128174031-69ecbb4d6d5d
	golang.org/x/net v0.0.0-20190930134127-c5a3c61f89f3
	//github.com/golang/oauth2 v0.0.0-20200107190931-bf48bf16ab8d
	golang.org/x/oauth2 v0.0.0-20200107190931-bf48bf16ab8d
	golang.org/x/sys v0.0.0-20191120155948-bd437916bb0e
	golang.org/x/text v0.3.2
	//github.com/golang/time v0.0.0-20191024005414-555d28b269f0
	golang.org/x/time v0.0.0-20191024005414-555d28b269f0
	golang.org/x/xerrors v0.0.0-20191204190536-9bdfabe68543
	google.golang.org/appengine v1.6.5
	gopkg.in/fsnotify.v1 v1.4.7
	gopkg.in/inf.v0 v0.9.1
	gopkg.in/tomb.v1 v1.0.0-20141024135613-dd632973f1e7
	gopkg.in/yaml.v2 v2.2.8
	k8s.io/api v0.15.11
	k8s.io/apimachinery v0.15.11
	k8s.io/client-go v0.15.11
	k8s.io/klog v1.0.0
	k8s.io/kube-openapi v0.0.0-20190816220812-743ec37842bf
	//github.com/kubernetes/utils v0.0.0-20200124190032-861946025e34
	k8s.io/utils v0.0.0-20200124190032-861946025e34
	sigs.k8s.io/controller-runtime v0.4.0
	sigs.k8s.io/yaml v1.2.0
)

//replace gopkg.in/fsnotify.v1 v1.4.7 => github.com/fsnotify/fsnotify v1.4.7

//replace golang.org/x/crypto v0.0.0-20200128174031-69ecbb4d6d5d => github.com/golang/crypto v0.0.0-20200128174031-69ecbb4d6d5d

///replace golang.org/x/oauth2 v0.0.0-20200107190931-bf48bf16ab8d => github.com/golang/oauth2 v0.0.0-20200107190931-bf48bf16ab8d

//replace golang.org/x/time v0.0.0-20191024005414-555d28b269f0 => github.com/golang/time v0.0.0-20191024005414-555d28b269f0

//replace k8s.io/utils v0.0.0-20200124190032-861946025e34 => github.com/kubernetes/utils v0.0.0-20200124190032-861946025e34
