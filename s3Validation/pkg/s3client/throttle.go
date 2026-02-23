package s3client

import (
	"bytes"
	"context"
	"io"
	"time"
)

// ThrottledReader wraps an io.ReadSeeker and limits the read speed.
// It implements io.ReadSeeker to support AWS SDK's retry and checksum mechanisms.
type ThrottledReader struct {
	reader      *bytes.Reader
	bytesPerSec int
	startTime   time.Time
	bytesRead   int64
	ctx         context.Context
}

// NewThrottledReader creates a reader that limits throughput to bytesPerSec.
func NewThrottledReader(ctx context.Context, data []byte, bytesPerSec int) *ThrottledReader {
	return &ThrottledReader{
		reader:      bytes.NewReader(data),
		bytesPerSec: bytesPerSec,
		startTime:   time.Now(),
		ctx:         ctx,
	}
}

func (t *ThrottledReader) Read(p []byte) (int, error) {
	// Check context cancellation
	select {
	case <-t.ctx.Done():
		return 0, t.ctx.Err()
	default:
	}

	// Calculate how long we should wait based on bytes read
	elapsed := time.Since(t.startTime)
	expectedDuration := time.Duration(float64(t.bytesRead) / float64(t.bytesPerSec) * float64(time.Second))

	if expectedDuration > elapsed {
		sleepTime := expectedDuration - elapsed
		select {
		case <-time.After(sleepTime):
		case <-t.ctx.Done():
			return 0, t.ctx.Err()
		}
	}

	// Limit chunk size to control granularity (10 chunks per second for smooth throttling)
	chunkSize := t.bytesPerSec / 10
	if chunkSize < 1024 {
		chunkSize = 1024
	}
	if len(p) > chunkSize {
		p = p[:chunkSize]
	}

	n, err := t.reader.Read(p)
	t.bytesRead += int64(n)

	return n, err
}

// Seek implements io.Seeker. Required by AWS SDK for retries and checksum computation.
func (t *ThrottledReader) Seek(offset int64, whence int) (int64, error) {
	// Reset throttle state on seek
	t.bytesRead = 0
	t.startTime = time.Now()
	return t.reader.Seek(offset, whence)
}

// Ensure ThrottledReader implements io.ReadSeeker
var _ io.ReadSeeker = (*ThrottledReader)(nil)
