# Build the manager binary
FROM golang:1.24 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

COPY go.mod go.mod
COPY go.sum go.sum

RUN go mod download

COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/ internal/
COPY pkg/ pkg/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

FROM alpine:3.19 AS runtime
ARG TARGETARCH

# Install AWS CLI v1 (easier to install in Alpine), ca-certificates, and other necessary packages
RUN apk add --no-cache \
    ca-certificates \
    bash \
    curl \
    python3 \
    py3-pip \
    && pip3 install --no-cache-dir --break-system-packages awscli \
    && aws --version

WORKDIR /
COPY --from=builder /workspace/manager .

RUN chmod +x /manager

# Run as root for AWS CLI access
USER 0

ENTRYPOINT ["/manager"]