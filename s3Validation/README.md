# S3 Validation Tool

A self-contained binary that validates S3 capabilities on any S3-compatible endpoint (AWS S3, MinIO, Ceph, etc.).

## Quick Start

```bash
# Run against AWS S3
AWS_ACCESS_KEY_ID=<key> \
AWS_SECRET_ACCESS_KEY=<secret> \
AWS_REGION=us-east-1 \
./s3validation -endpoint s3.us-east-1.amazonaws.com -bucket <your-bucket>

# Run against MinIO
AWS_ACCESS_KEY_ID=minioadmin \
AWS_SECRET_ACCESS_KEY=minioadmin123 \
AWS_REGION=us-east-1 \
./s3validation -endpoint http://localhost:9000 -bucket <your-bucket>
```

## Command Line Options

```
Usage: s3validation -endpoint <endpoint> -bucket <bucket> [-v] [-json]

Options:
  -endpoint  S3 endpoint URL (required)
  -bucket    S3 bucket name (required)
  -v         Verbose output with debug details
  -json      Output results as JSON for automation
```

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `AWS_ACCESS_KEY_ID` | Yes | AWS access key |
| `AWS_SECRET_ACCESS_KEY` | Yes | AWS secret key |
| `AWS_REGION` | Yes | AWS region (e.g., us-east-1) |
| `AWS_SESSION_TOKEN` | No | Session token for temporary credentials |

---

## Developer Guide

### Prerequisites

- Go 1.21+
- Docker & Docker Compose (for local MinIO testing)

### Project Structure

```
├── main.go              # Test runner binary
├── pkg/s3client/        # S3 client wrapper
│   ├── client.go        # S3 operations
│   ├── credentials.go   # Credential loading
│   └── throttle.go      # Bandwidth throttling
├── assets/              # Embedded test data
├── release/             # Built binaries
├── docker-compose.yml   # Local MinIO setup
└── AGENTS.md            # AI agent instructions
```

### Building Binaries

Build for all supported platforms:

```bash
# Create release directory
mkdir -p release

# macOS ARM64 (Apple Silicon)
go build -ldflags="-s -w" -o release/s3validation-darwin-arm64 .

# Linux x86_64 (Intel/AMD servers)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o release/s3validation-linux-amd64 .

# Linux ARM64 (AWS Graviton, ARM servers)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o release/s3validation-linux-arm64 .
```

Or build all at once:

```bash
mkdir -p release && \
go build -ldflags="-s -w" -o release/s3validation-darwin-arm64 . && \
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o release/s3validation-linux-amd64 . && \
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o release/s3validation-linux-arm64 .
```

### Local Testing with MinIO

#### 1. Start MinIO

```bash
docker compose up -d
```

This starts:
- MinIO server on `http://localhost:9000`
- MinIO console on `http://localhost:9001`
- Creates bucket `s3-validation-tests-72n80c6a`

#### 2. Run Tests Against MinIO

```bash
AWS_ACCESS_KEY_ID=minioadmin \
AWS_SECRET_ACCESS_KEY=minioadmin123 \
AWS_REGION=us-east-1 \
go run . -endpoint http://localhost:9000 -bucket s3-validation-tests-72n80c6a -v
```

Or with a built binary:

```bash
AWS_ACCESS_KEY_ID=minioadmin \
AWS_SECRET_ACCESS_KEY=minioadmin123 \
AWS_REGION=us-east-1 \
./release/s3validation-darwin-arm64 -endpoint http://localhost:9000 -bucket s3-validation-tests-72n80c6a -v
```

#### 3. Stop MinIO

```bash
docker compose down
```

To remove all data:

```bash
docker compose down -v
```

### Testing Against AWS S3

#### 1. Set Credentials

```bash
export AWS_ACCESS_KEY_ID=<your-key>
export AWS_SECRET_ACCESS_KEY=<your-secret>
export AWS_REGION=us-east-1
```

#### 2. Create Test Bucket (one-time)

```bash
aws s3 mb s3://your-test-bucket
```

#### 3. Run Tests

```bash
go run . -endpoint s3.us-east-1.amazonaws.com -bucket your-test-bucket -v
```

### Testing Linux Binaries in Docker

Test the Linux binaries in a minimal RHEL9 container:

```bash
# Linux AMD64
docker run --rm \
  -v "$(pwd)/release/s3validation-linux-amd64:/s3validation" \
  -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_REGION \
  --platform linux/amd64 \
  registry.access.redhat.com/ubi9-minimal:latest \
  /s3validation -endpoint s3.us-east-1.amazonaws.com -bucket your-bucket

# Linux ARM64
docker run --rm \
  -v "$(pwd)/release/s3validation-linux-arm64:/s3validation" \
  -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_REGION \
  --platform linux/arm64 \
  registry.access.redhat.com/ubi9-minimal:latest \
  /s3validation -endpoint s3.us-east-1.amazonaws.com -bucket your-bucket
```

### Adding New Tests

See [AGENTS.md](AGENTS.md) for detailed instructions on adding new tests.

## License

Internal use only.
