# Copyright 2026 Luca Burgazzoli
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

# Build stage: pinned to the build host's native architecture so the
# generator and toolchain never run under emulation. Only the final
# compile targets the requested platform.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27 AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# Module manifests first, so dependency downloads stay cached across
# source-only changes.
COPY go.mod go.sum ./
RUN go mod download

COPY Makefile ./
COPY hack/ ./hack/
COPY pkg/ ./pkg/
COPY cmd/ ./cmd/
COPY config/crd/ ./config/crd/

# Regenerate the CRD that config/crd embeds at compile time, so the
# image never depends on a source checkout having run the generator.
RUN make manifests

# Static CLI binary for the requested target platform.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -o /out/odh-platform-tools ./cmd

# Prepare the runtime HOME here, in the native builder: the runtime
# stage below must not execute any command, so cross-builds for a
# foreign architecture never need emulation.
RUN install -d -m 0750 -o 1001 -g 1001 /out/home/default

# Runtime stage: UBI10 minimal, no Go toolchain, non-root CLI.
# No RUN instructions here on purpose (see above).
FROM registry.access.redhat.com/ubi10/ubi-minimal

COPY --from=build --chown=1001:1001 /out/home/ /home/
COPY --from=build /out/odh-platform-tools /usr/local/bin/odh-platform-tools

ENV HOME=/home/default
USER 1001
ENTRYPOINT ["/usr/local/bin/odh-platform-tools"]
