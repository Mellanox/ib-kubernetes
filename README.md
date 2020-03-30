[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](http://www.apache.org/licenses/LICENSE-2.0)
[![Build Status](https://travis-ci.com/Mellanox/ib-kubernetes.svg?branch=master)](https://travis-ci.com/Mellanox/ib-kubernetes)
[![Go Report Card](https://goreportcard.com/badge/github.com/Mellanox/ib-kubernetes)](https://travis-ci.com/Mellanox/ib-kubernetes)
[![Coverage Status](https://coveralls.io/repos/github/Mellanox/ib-kubernetes/badge.svg)](https://coveralls.io/github/Mellanox/ib-kubernetes)

   * [InfiniBand Kubernetes](#infiniband-kubernetes)
      * [Subnet Manager Plugins](#subnet-manager-plugins)
      * [Build](#build)
         * [Building InfiniBand Kubernetes Binary](#building-infiniband-kubernetes-binary)
         * [Building Subnet Manager Plugins](#building-subnet-manager-plugins)
         * [Building Container Image](#building-container-image)
      * [Configuration Reference](#configuration-reference)
      * [Plugins](#plugins)
         * [NOOP Plugin](#noop-plugin)
         * [UFM (Unified Fabric Manager) Plugin](#ufm-plugin)
      * [Deployment](#deployment)

# InfiniBand Kubernetes

InfiniBand Kubernetes provides a daemon `ib-kubernetes`, that works in conjuction with [Mellanox InfiniBand SR-IOV CNI](https://github.com/Mellanox/ib-sriov-cni) and [Intel Multus CNI](https://github.com/intel/multus-cni), it acts on kubernetes Pod object changes(Create/Update/Delete), reading the Pod's network annotation and fetching its corresponding network CRD and and reads the PKey, to add the newly generated Guid or the predefined Guid in `guid` field of CRD `cni-args` to that PKey, for pods with annotation `mellanox.infiniband.app`.

## Subnet Manager Plugins

InifiBand Kubernets uses [Golang plugins](https://golang.org/pkg/plugin/) to communicate with the fabric subnet manager 
Subnet manager plugins exists in `pkg/sm/plugins`. There are currently 2 plugins:

1. UFM Plugin
2. Noop Plugin

## Build

To build InfiniBand Kubernetes use the makefile.

### Building InfiniBand Kubernetes Binary

To build only the binary for InfiniBand Kubernetes

```shell script
$ make build
```
Upon successful build the binary will be available in `build/ib-kubernetes`.

### Building Subnet Manager Plugins

To build all the plugins binaries for InfiniBand Kubernetes that exist in `pkg/sm/plugins`

#### Building all plugins
```
$ make plugins
```

#### Building a specific plugin
```
make <plugin name>-plugin
```
Example:
```
$ make <>-plugin
```
Upon successful build the plugins binaries will be available in `build/plugins/`.

Note: to build all binaries at once run `make`.

### Building Container Image

To build container image

#### Building image mellanox/ib-kubernetes
```
$ make image
```

#### Building image with custom tag and Dockerfile
```
$ DOCKERFILE=myfile TAG=mytag make image
```

## Configuration Reference

IB Kubernetes configration as ConfigMap :
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: ib-kubernetes-config
  namespace: kube-system
data:
  DAEMON_SM_PLUGIN: "ufm" # Name of the subnet manager plugin
  DAEMON_PERIODIC_UPDATE: "5" # Interval in seconds to send add and remove request to subnet manager
  GUID_POOL_RANGE_START: "02:00:00:00:00:00:00:00" # The first guid in the pool
  GUID_POOL_RANGE_END: "02:FF:FF:FF:FF:FF:FF:FF" # The last guid in the pool
```

## Plugins

Subnet Manager Plugin to configure PKeys (Partition Keys) in the InfiniBand fabric.

### NOOP Plugin

Plugin that does nothing. Example for developing user subnet manager plugin

### UFM (Unified Fabric Manager) Plugin

[UFM](https://www.mellanox.com/products/management-software/ufm) is a powerful platform for managing scale-out computing environments.
UFM Plugin allow to configure PKeys (Partition Keys) via UFM.

#### Plugin Configuration

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: ib-kubernetes-ufm-secret
  namespace: kube-system
stringData:
  UFM_USERNAME: "admin"  # UFM Username
  UFM_PASSWORD: "123456" # UFM Password
  UFM_ADDRESS: ""        # UFM Hostname/IP Address 
  UFM_HTTP_SCHEMA: ""    # http/https. Default: https
  UFM_PORT: ""           # UFM REST API port. Defaults: 443(https), 80(http)
string:
  UFM_CERTIFICATE: ""    # UFM Certificate in base64 format. (if not provided client will not verify server's certificate chain and host name)
```

## Deployment

To deploy the InfiniBand Kbubernetes
```
$ kubectl create -f deployment/ib-kubernetes-configmap.yaml
$ kubectl create -f deployment/ib-kubernetes-ufm-secret.yaml
$ kubectl create -f deployment/ib-kubernetes.yaml
```

## Limitations

- Each node in an Infiniband Kubernetes deployment may be associated with up to 128 PKeys due to kernel limitation.
