FROM golang:1.18.3-alpine3.16 as builder

# Install:
# - Git for pulling dependencies with go modules.
# - Shadow to create unprivileged user.
RUN apk add git shadow make

WORKDIR /usr/src/metallb-hcloud-controller

# Prepare directory for source code and empty directory, which we copy
# to scratch image.
RUN mkdir tmp

# Copy go mod files first and install dependencies to cache this layer.
COPY ./go.mod ./go.sum ./
RUN go mod download

# Add source code.
COPY . .

# Build and run tests.
RUN make build

FROM scratch

# Copy executable.
COPY --from=builder /usr/src/metallb-hcloud-controller/metallb-hcloud-controller /

# Required for running as nobody.
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group

# Required when talking TLS to the outside world (Hetzner API in this case).
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Use unprivileged user.
USER nobody

# By default run server.
ENTRYPOINT ["/metallb-hcloud-controller"]
