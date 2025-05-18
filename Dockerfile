# Build stage
FROM golang:1.22 AS builder

WORKDIR /app

# Install build dependencies
RUN apt-get update && apt-get install -y \
    gcc \
    libc6-dev \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Copy go mod and sum files
COPY go.mod go.sum ./

# Copy source code
COPY . .

# Build the application with CGO enabled
RUN CGO_ENABLED=1 GOOS=linux go build -o supervisor

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
COPY --from=builder /app/supervisor /usr/local/bin/

# Copy and set up the policy script
COPY scripts/set-policy.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/set-policy.sh

# Expose the supervisor port
EXPOSE 8080

# Use supervisor as the entrypoint with --target flag
ENTRYPOINT ["/usr/local/bin/supervisor", "--target", "http://localhost:8080"]

# Use CMD to pass additional arguments
CMD ["$@"] 