# Stage 1: Build binary
FROM --platform=$BUILDPLATFORM golang:alpine AS builder

ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /app

# Install git and ca-certificates
RUN apk add --no-cache git ca-certificates tzdata

# Cache go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build pure Go binary without CGO
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-w -s" -o /app/analyzer ./cmd/analyzer

# Stage 2: Minimal runtime image
FROM alpine:latest

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata

# Copy binary from builder
COPY --from=builder /app/analyzer /app/analyzer

# Expose analyzer port
EXPOSE 8080

ENTRYPOINT ["/app/analyzer"]
