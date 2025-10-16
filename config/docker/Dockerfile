# Production stage with binary execution
FROM golang:1.24-alpine AS builder

# Install git and build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Copy environment files
COPY config/env/ /app/config/env/

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o git-executor ./cmd/git-executor

# Final stage
FROM alpine:latest

# Install runtime dependencies
RUN apk --no-cache add ca-certificates tzdata git

# Create non-root user
RUN addgroup -g 1001 -S gituser && \
    adduser -u 1001 -S gituser -G gituser

# Set working directory
WORKDIR /app

# Copy the binary from builder stage
COPY --from=builder /app/git-executor .

# Copy environment files from builder stage
COPY --from=builder /app/config/env/ /app/config/env/

# Create necessary directories with proper permissions
RUN mkdir -p /var/lib/git/repos /config /tmp /specs && \
    chown -R gituser:gituser /app /var/lib/git/repos /config /tmp /specs  && \
    chmod -R 777 /var/lib/git/repos
    
# Switch to non-root user (commented out for debugging)
# USER gituser

# Expose the default port
EXPOSE 5001 5222

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD ["./git-executor", "--health-check"] || exit 1

# Run the binary
CMD ["./git-executor"]
