# ==============================================================================
# Multi-stage Dockerfile: WA Bot QA Workbench & Live Chat Gateway
# ==============================================================================

# Stage 1: Build binary with CGO enabled (required for SQLite session store)
FROM golang:alpine AS builder

WORKDIR /build

# Install compilation toolchain for CGO (mattn/go-sqlite3)
RUN apk add --no-cache gcc g++ musl-dev git

# Cache dependency layer
COPY go.mod go.sum ./
RUN go mod download

# Copy source code including embedded public assets
COPY . .

# Compile optimized binary
RUN CGO_ENABLED=1 GOOS=linux go build \
    -ldflags="-s -w" \
    -o wa-bot-tester main.go

# Stage 2: Minimal runtime image
FROM alpine:3.20

WORKDIR /app

# Install runtime utilities (SSL certificates for HTTPS, timezone data, SQLite libraries)
RUN apk add --no-cache ca-certificates tzdata sqlite-libs

# Set timezone to Asia/Jakarta (WIB)
ENV TZ=Asia/Jakarta

# Create storage directory for WA SQLite sessions & attachments
RUN mkdir -p /app/data

# Copy binary from build stage
COPY --from=builder /build/wa-bot-tester /app/wa-bot-tester

# Expose application port
EXPOSE 3020

# Command to execute
CMD ["/app/wa-bot-tester"]
