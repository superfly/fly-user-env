# Fly User Environment

A long-running service that runs in a Docker container and handles HTTP requests.

## Features

- Runs as a service in a Docker container
- Handles HTTP requests and proxies them to the target application
- Provides an admin interface for configuration
- Supports process supervision and automatic restarts
- Manages file system state with JuiceFS
- Handles database replication with Litestream

## Usage

```bash
fly-user-env [flags] command [args...]
```

### Flags

- `--listen`: Address to listen on (default: "0.0.0.0:8080")
- `--target`: Address to proxy requests to (default: "127.0.0.1:3000")
- `--token`: Authentication token for the admin interface (default: "test-token")

### Examples

Run a Python HTTP server:
```bash
fly-user-env --target 127.0.0.1:3000 python -m http.server 3000
```

Run a Node.js application:
```bash
fly-user-env --listen 0.0.0.0:9090 --target 127.0.0.1:3000 node server.js
```

Run a custom application:
```bash
fly-user-env --target 127.0.0.1:3000 ./myapp --config config.yaml --debug
```

## Configuration

The service can be configured through the admin interface. Configuration options include:

- Object storage settings (S3-compatible)
- Database replication settings
- File system settings
- Process management settings

## Building

### Local Build

```bash
go build -o fly-user-env
```

### Docker Build

```bash
docker build -t fly-user-env .
```

## Running with Docker

```bash
docker run -p 8080:8080 fly-user-env --target 127.0.0.1:3000 your-command
```

## Features

- The service will start the specified command and monitor its output
- If the command exits, the service will attempt to restart it
- HTTP requests are proxied to the target application
- The admin interface is available at `/admin` with the configured token
- File system state is managed with JuiceFS
- Database replication is handled with Litestream

## Development

### Prerequisites

- Go 1.22 or later
- Docker
- FUSE support (for JuiceFS)
- AWS CLI (for S3 operations)

### Running Tests

```bash
go test ./...
```

### Running Integration Tests

```bash
go test -v ./tests/...
```

## Behavior

- The service will start the specified command and monitor its output
- If the command exits, the service will attempt to restart it
- HTTP requests are proxied to the target application
- The admin interface is available at `/admin` with the configured token
- File system state is managed with JuiceFS
- Database replication is handled with Litestream

## Configuration

- `PORT` - Environment variable to set the server port (default: 8080)

## API Endpoints

- `GET /health` - Health check endpoint 