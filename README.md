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
      * [Deployment](#deployment)

# InfiniBand Kubernetes

InfiniBand Kubernetes provides a daemon `ib-kubernetes`, that works in conjuction with [Mellanox InfiniBand SR-IOV CNI](https://github.com/Mellanox/ib-sriov-cni) and [Intel Multus CNI](https://github.com/intel/multus-cni), it acts on kubernetes Pod object changes(Create/Update/Delete), reading the Pod's network annotation and fetching its corresponding network CRD and and reads the PKey, to add the newly generated Guid or the predefined Guid in `guid` field of CRD `cni-args` to that PKey, for pods with annotation `mellanox.infiniband.app`.

## Subnet Manager Plugins

InifiBand Kubernets uses [Golang plugins](https://golang.org/pkg/plugin/) to add the guids to PKey subnet manager. 
Subnet manager plugins exists in `pkg/sm/plugins`. There are currently 2 plugins:

1. UFM Plugin: This plugin communicate with [Mellanox UFM ](https://www.mellanox.com/products/management-software/ufm) rest api to add the Generated Guids to PKey.
2. Noop Plugin: This plugin doesn't do any special operations, it can be used as template for developing user's own plugin.

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

```shell script
# building all plugins
$ make plugins

# building one plugin, make <plugin-name>-plugin
$ make noop-plugin
```
Upon successful build the plugins binaries will be available in `build/plugins/`.

Note: to build all binaries at once run `$ make`.

### Building Container Image

To build container image

```shell script
# Building image mellanox/ib-kubernetes
$ make image

# Building image with custom tag and Dockerfile
$ DOCKERFILE=myfile TAG=mytag make image
```

## Configuration Reference

User can provide the following configurations as environment variables or for the ConfigMap :
* PLUGIN: Name of the subnet manager plugin, currently supported "noop" and "ufm".
* PERIODIC_UPDATE: Interval in seconds to send add and remove request to subnet manager.
* RANGE_START: The first guid in the pool to generated, e.g: "02:00:00:00:00:00:00:00".
* RANGE_END: The Last guid in the pool.

**Configurations if "ufm" subnet manager plugin is used for  `deployment/ib-kubernetes-ufm-secret.yaml`:**
* UFM_USERNAME: Username of UFM. 
* UFM_PASSWORD: Password of UFM.
* UFM_ADDRESS: IP address or hostname of UFM server.
* UFM_HTTP_SCHEMA: http/https, default is https.
* UFM_PORT: REST API port of UFM default is 443, if `httpSchema` is http then default is 80.
* UFM_CERTIFICATE: Secure certificate if using secure connection.

## Deployment

To deploy the InfiniBand Kbubernetes
```shell script
$ kubectl create -f deployment/ib-kubernetes-configmap.yaml
$ kubectl create -f deployment/ib-kubernetes-ufm-secret.yaml
$ kubectl create -f deployment/ib-kubernetes-ds.yaml
```
