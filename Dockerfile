# Copyright 2025 NVIDIA CORPORATION & AFFILIATES
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# SPDX-License-Identifier: Apache-2.0

FROM golang:1.24 AS builder

WORKDIR /workspace
ADD ./go.mod ./
ADD ./go.sum ./
RUN go mod download

ADD ./ ./
RUN make all

FROM nvcr.io/nvidia/distroless/go:v3.1.11
LABEL org.opencontainers.image.source=https://nvcr.io/nvidia/cloud-native/multus-cni
WORKDIR /
# Copy the built binary and plugins from the builder stage
COPY --from=builder /workspace/build/ib-kubernetes /
COPY --from=builder /workspace/build/plugins /plugins
# Copy source code for open source compliance
COPY . /workspace

LABEL io.k8s.display-name="InfiniBand Kubernetes"

CMD ["/ib-kubernetes"]
