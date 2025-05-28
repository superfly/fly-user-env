# State Manager

A process management and state persistence system written in Go. It manages long-running processes, handles state persistence, and implements checkpointing and process recovery.

## Core Features

### 1. Process Supervision
- Process monitoring and management
- Automatic process restart on failure
- Graceful shutdown with configurable timeouts
- Process status reporting

### 2. State Management
- System state checkpoint creation and restoration
- SQLite database management with replication
- S3-compatible object storage integration

### 3. HTTP Control Interface
- HTTP endpoints for system control
- Initial server configuration
- System status reporting

## Architecture

### Core Components

1. **Supervisor**
   - Process lifecycle management
   - Process monitoring
   - Signal handling
   - Configurable restart delays

2. **Control Interface**
   - HTTP server
   - Configuration management
   - Status reporting
   - Checkpoint operations

3. **Database Manager**
   - SQLite database operations
   - Replication management
   - State persistence

4. **Object Storage Integration**
   - S3-compatible storage operations
   - Configurable endpoints
   - State backup and restore

## Usage

### Starting the System
```bash
./state-manager
```

### Configuration
The system uses a JSON configuration file with the following structure. The server can run in an unconfigured state and be configured later through the API:

```json
{
  "storage": {
    "bucket": "your-bucket",
    "endpoint": "your-endpoint",
    "access_key": "your-access-key",
    "secret_key": "your-secret-key",
    "region": "your-region",
    "key_prefix": "your-prefix",
    "env_dir": "your-env-dir"
  },
  "stacks": ["component1", "component2"]
}
```

### Configuration Flow
1. The server can start in an unconfigured state
2. Initial configuration can be applied through the API
3. Once configured, the system will persist the configuration
4. Configuration changes require server restart

### Supervisor Configuration
- `TimeoutStop`: Graceful shutdown timeout (default: 90s)
- `RestartDelay`: Process restart delay (default: 1s)

## API Endpoints

### Control Interface
- `GET /`: System status
- `GET /config`: Current configuration
- `POST /config`: Initial configuration setup (only works on unconfigured server)
- `POST /checkpoint`: Create system checkpoint
- `POST /restore`: Restore from checkpoint
- `POST /release-lease`: Release system lease

## Process Management

1. **Process Recovery**
   - Automatic restart on process exit
   - Configurable restart delays
   - Process status monitoring

2. **State Persistence**
   - Checkpoint creation
   - Database replication
   - Object storage backup

3. **Shutdown Process**
   - Configurable shutdown timeouts
   - Signal handling
   - Process termination

## Monitoring
- HTTP interface for system status
- Process health monitoring
- Database replication status

## Security

1. **Authentication**
   - Token-based API authentication
   - Credential management

2. **Data Protection**
   - Configuration file security
   - API communication

## Operations

1. **Configuration**
   - Environment variable usage
   - Configuration backup
   - Change documentation

2. **Monitoring**
   - Health checks
   - Log monitoring
   - Checkpoint tracking

3. **Maintenance**
   - Checkpoint cleanup
   - Storage monitoring
   - Credential updates

## Error Handling

The system implements error handling for:
- Process failures
- Configuration errors
- Storage operations
- Network operations

## Limitations

1. **Current Limitations**
   - Database manager is not checkpointable
   - S3-compatible storage only
   - Single process supervision

2. **Known Issues**
   - Check documentation for current known issues
   - Monitor GitHub issues for updates

## Development

### Prerequisites
- Go 1.22 or later
- Docker
- FUSE support (for JuiceFS)
- AWS CLI (for S3 operations)

### Building

#### Local Build
```bash
go build -o state-manager
```

#### Docker Build
```bash
docker build -t state-manager .
```

### Running Tests
```bash
go test ./...
```

### Running Integration Tests
```bash
go test -v ./tests/...
```

## Support

For issues and support:
1. Check the documentation
2. Review GitHub issues
3. Contact the development team

## Contributing

Contributions are welcome:
1. Fork the repository
2. Create a feature branch
3. Submit a pull request
4. Follow the contribution guidelines 