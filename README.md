[![Build Status](https://travis-ci.com/Mmduh-483/ib-kubernetes.svg?branch=master)](https://travis-ci.com/Mmduh-483/ib-kubernetes) [![Coverage Status]
(https://coveralls.io/repos/github/Mmduh-483/ib-kubernetes/badge.svg)](https://coveralls.io/github/Mmduh-483/ib-kubernetes)

   * [InfiniBand Kubernetes](#infiniband-kubernetes)
      * [Subnet Manager Plugins](#subnet-manager-plugins)
      * [Build](#build)
         * [Building InfiniBand Kubernetes Binary] (#building-infiniband-kubernetes-binary)
         * [Building Subnet Manager Plugins] (#building-subnet-manager-plugins)
         * [Building Container Image] (#building-container-image)
      * [Configuration reference](#configuration-reference)
      * [Deploying](#deploying)

# IB-Kubernetes

InfiniBand Kubernetes cooperate with [Mellanox InfiniBand SR-IOV CNI](https://github.com/Mellanox/ib-sriov-cni) and 
[Intel Multus CNI](https://github.com/intel/multus-cni), where the InfiniBand Kubernetes read the InfiniBand SR-IOV CNI 
network attachment configuration created for Multus CNI and read the PKey, to add the newly generated Guids to that PKey,
for pods with annotation `mellanox.infiniband.app`.

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

## Configuration

User can provide the following configurations as environment variables or for the ConfigMap :
* GUID_RANGE_START: It is a guid hardware address, which is the first guid in the pool to generated.
* GUID_RANGE_END: Last guid in the pool.
* SUB_NET_MANAGER_SECRET_NAMESPACE: Namespace subnet manager secret.
* SUB_NET_MANAGER_SECRET_CONFIG_NAME: Name of the secret that contains the subnet manager configuration.
* SUB_NET_MANAGER_PLUGIN: Name of the subnet manager plugin, currently supported "noop" and "ufm".
* PERIODIC_UPDATE: Period to send add and remove request to ufm.

To create a secret for UFM config check the example bellow. For more details about Kubernetes Secret check the [documentation](https://kubernetes.io/docs/concepts/configuration/secret/).

```shell script
$ cat > config <<EOF
{
    "username": "admin",
    "password": "123456",
    "address": "192.168.1.1",
    "port": 80,
    "httpSchema": "http"
}
EOF

$ kubectl create secret generic ufm-config-secret --from-file=config --namespace kube-system
```

Note: the key name that contain configuration should be `config`.

UFM config:
* `username`: (string, required) username of UFM.
* `password`: (string, required) password of UFM.
* `address`: (string, required) IP address or hostname of UFM.
* `httpSchema`: (string, optional) http/https, default is https.
* `port`: (int, optional) Port number where UFM REST API run, default is 443, if `httpSchema` is http then default is 80.
* `certificate`: (string, optional) Secure certificate if using secure connection.

## Deploying

To deploy the InfiniBand Kbubernetes
```shell script
$ kubectl create -f deployment/ib-kubernetes.yaml

# for kubernetes version prior 1.16
$ kubectl create -f deployment/ib-kubernetes-pre-1.16.yaml
```
