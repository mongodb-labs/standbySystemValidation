# S3 Validation Binary - Agent Guide

## Project Goal

This project produces a **self-contained binary** that customers run on their environments to validate S3 capabilities. Customers may use AWS S3 or any S3-compatible endpoint (MinIO, Ceph, etc.). The binary must work without requiring Go, Python, or other runtimes installed.

## Architecture

```
main.go           → Test runner binary (what customers use)
pkg/s3client/     → S3 client wrapper with helper methods
assets/           → Embedded test data (compiled into binary)
release/          → Built binaries for distribution
docker-compose.yml → Local MinIO for development testing
```

## Adding New Tests

### 1. Implement the test function in `main.go`

Follow this pattern:

```go
func testYourNewTest(ctx context.Context, tc *TestContext) TestResult {
    start := time.Now()
    result := TestResult{Details: []string{}}

    // Your test logic here
    // Use tc.client for S3 operations
    // Use tc.bucket for bucket name
    // Append to result.Details for verbose output

    if somethingFailed {
        result.Duration = time.Since(start)
        result.Error = "Description of what failed"
        return result
    }

    result.Duration = time.Since(start)
    result.Passed = true
    return result
}
```

### 2. Register the test in the `tests` slice in `main()`

```go
tests := []struct {
    name string
    fn   func(context.Context, *TestContext) TestResult
}{
    // ... existing tests ...
    {"YourNewTest", testYourNewTest},  // Add here
}
```

### 3. Use unique object keys

Each test should use a unique object key to avoid conflicts. Define constants at the top:

```go
const yourTestObjectKey = "your-test-object.txt"
```

## S3 Client Methods Available

| Method | Purpose |
|--------|---------|
| `PutObject(ctx, bucket, key, data)` | Upload object |
| `GetObject(ctx, bucket, key)` | Download object |
| `HeadObject(ctx, bucket, key)` | Get ETag/metadata without body |
| `DeleteObject(ctx, bucket, key)` | Delete object |
| `ObjectExists(ctx, bucket, key)` | Check if object exists |
| `ListObjects(ctx, bucket, prefix)` | List objects with prefix |
| `PutObjectThrottled(ctx, bucket, key, data, bytesPerSec)` | Rate-limited upload |
| `PutObjectIfMatch(ctx, bucket, key, data, etag)` | Conditional update |
| `PutObjectIfNoneMatch(ctx, bucket, key, data)` | Conditional create |
| `MultipartUpload(ctx, bucket, key, parts)` | Multipart upload |

## Development Testing

### Prerequisites

```bash
# Start MinIO
docker compose up -d

# Wait for bucket creation (~5 seconds)
```

### Run against MinIO

```bash
AWS_ACCESS_KEY_ID=minioadmin \
AWS_SECRET_ACCESS_KEY=minioadmin123 \
AWS_REGION=us-east-1 \
./release/s3validation-darwin-arm64 -endpoint http://localhost:9000 -bucket s3-validation-tests-72n80c6a -v
```

### Run against AWS S3

```bash
# Set your AWS credentials
export AWS_ACCESS_KEY_ID=<your-key>
export AWS_SECRET_ACCESS_KEY=<your-secret>
export AWS_REGION=us-east-1

# Create a test bucket first (one-time)
aws s3 mb s3://your-test-bucket

./release/s3validation-darwin-arm64 -endpoint s3.us-east-1.amazonaws.com -bucket your-test-bucket -v
```

### Build binaries

Build for all supported platforms and place in `release/` directory:

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

## Coding Conventions

1. **No external test frameworks** - Tests run via `main.go`, not `go test`
2. **Self-contained binary** - Embed any test data using `//go:embed` in `assets/`
3. **Verbose logging** - Use `result.Details` for debug info shown with `-v`
4. **Error messages** - Set `result.Error` with clear failure description
5. **Cleanup** - Delete any unique/temporary objects created during tests
6. **Timeouts** - Use context with timeout (default 5 minutes in main)
7. **MinIO compatibility** - All tests must pass on both AWS S3 and MinIO

## Error Handling

Use sentinel errors from `pkg/s3client/client.go`:

```go
s3client.ErrPreconditionFailed  // Conditional PUT failed (ETag mismatch)
s3client.ErrObjectAlreadyExists // IfNoneMatch failed (object exists)
```

## Output Formats

The binary supports two output modes:
- **Human-readable** (default) - For manual testing
- **JSON** (`-json` flag) - For CI/CD integration

Both modes must be kept in sync when modifying test output.
