# Build stage
FROM golang:1.22 AS builder

WORKDIR /app

# Install build dependencies
RUN apt-get update && apt-get install -y \
    gcc \
    libc6-dev \
    ca-certificates \
    fuse \
    curl \
    awscli \
    && rm -rf /var/lib/apt/lists/*

# Install JuiceFS
RUN curl -L https://github.com/juicedata/juicefs/releases/download/v1.1.2/juicefs-1.1.2-linux-amd64.tar.gz | tar xz \
    && mv juicefs /usr/local/bin/ \
    && chmod +x /usr/local/bin/juicefs

# Copy go mod and sum files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application with CGO enabled and version information
ARG VERSION=dev
ARG BUILD_TIME
ARG GIT_COMMIT
RUN CGO_ENABLED=1 GOOS=linux go build \
    -ldflags "-X main.Version=${VERSION} \
              -X main.BuildTime=${BUILD_TIME} \
              -X main.GitCommit=${GIT_COMMIT}" \
    -o fly-user-env

# Final stage
FROM ubuntu:22.04
RUN apt-get update && apt-get install -y \
    crun \
    ca-certificates \
    sqlite3 \
    apparmor \
    && rm -rf /var/lib/apt/lists/*

# Create necessary directories
RUN mkdir -p /var/lib/supervisor/container

# Set the working directory
WORKDIR /var/lib/supervisor/container

# Copy the binary
COPY --from=builder /app/fly-user-env /usr/local/bin/

# Copy and set up the policy script
COPY scripts/set-policy.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/set-policy.sh

# Expose the supervisor port
EXPOSE 8080

# Use supervisor as the entrypoint with --target flag
ENTRYPOINT ["/usr/local/bin/supervisor", "--target", "http://localhost:8080"]

# Use CMD to pass additional arguments
CMD ["$@"] 