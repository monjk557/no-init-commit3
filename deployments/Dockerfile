# Build stage
FROM golang:1.23-alpine AS builder

# Install git for go mod download
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-w -s -extldflags "-static"' \
    -a -installsuffix cgo \
    -o bin/git-executor \
    ./cmd/git-executor

# Final stage
FROM scratch

# Copy CA certificates from builder stage
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy timezone data
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy the binary
COPY --from=builder /app/bin/git-executor /git-executor

# Copy default configuration
COPY --from=builder /app/config/config.yaml.example /config/config.yaml.example

# Create directories for repositories and configuration
# Note: We'll create these as volumes in docker-compose
VOLUME ["/repos", "/config"]

# Expose the default port (overrideable via configuration)
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD ["/git-executor", "--health-check"] || exit 1

# Run as non-root user (using numeric ID for scratch image compatibility)
USER 65534:65534

# Set entrypoint
ENTRYPOINT ["/git-executor"]

# Default command
CMD ["--config", "/config/config.yaml"] 