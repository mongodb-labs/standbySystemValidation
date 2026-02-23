package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"sync"
	"time"

	"s3validation/assets"
	"s3validation/pkg/s3client"
)

// Test constants
const (
	testObjectKey            = "validation-test-object.txt"
	testPrefixObjectKey      = "validation-prefix/validation-object.txt"
	consistencyTestObjectKey = "consistency-test-object.bin"
	multipartTestObjectKey   = "multipart-test-object.txt"
	conditionalTestObjectKey = "conditional-test-object.txt"
	listObjectsTestPrefix    = "list-objects-test/"
	throttleBytesPerSec      = 750 * 1024 // 750 KB/s
)

// TestResult represents the result of a single test
type TestResult struct {
	Name     string        `json:"name"`
	Passed   bool          `json:"passed"`
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error,omitempty"`
	Details  []string      `json:"details,omitempty"`
}

// TestReport is the complete test run report
type TestReport struct {
	Endpoint   string        `json:"endpoint"`
	Bucket     string        `json:"bucket"`
	StartTime  time.Time     `json:"start_time"`
	EndTime    time.Time     `json:"end_time"`
	Duration   time.Duration `json:"duration"`
	TotalTests int           `json:"total_tests"`
	Passed     int           `json:"passed"`
	Failed     int           `json:"failed"`
	Results    []TestResult  `json:"results"`
}

// TestContext holds shared test state
type TestContext struct {
	client   *s3client.Client
	bucket   string
	endpoint string
	verbose  bool
}

// ETagObservation for consistency test
type ETagObservation struct {
	Timestamp time.Time
	ETag      string
	ReaderID  int
}

func main() {
	// Parse command-line arguments
	endpoint := flag.String("endpoint", "", "S3 endpoint (e.g., s3.us-east-1.amazonaws.com or http://localhost:9000)")
	bucket := flag.String("bucket", "", "S3 bucket name")
	verbose := flag.Bool("v", false, "Verbose output with debug logs")
	jsonOutput := flag.Bool("json", false, "Output results as JSON")
	flag.Parse()

	if *endpoint == "" || *bucket == "" {
		fmt.Println("S3 Validation Test Suite")
		fmt.Println("========================")
		fmt.Println()
		fmt.Println("Usage: s3validation -endpoint <endpoint> -bucket <bucket> [-v] [-json]")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -endpoint  S3 endpoint URL (required)")
		fmt.Println("             Examples: s3.us-east-1.amazonaws.com")
		fmt.Println("                       http://localhost:9000 (MinIO)")
		fmt.Println("  -bucket    S3 bucket name (required)")
		fmt.Println("  -v         Verbose output with debug logs")
		fmt.Println("  -json      Output results as JSON (for automation)")
		fmt.Println()
		fmt.Println("Environment variables:")
		fmt.Println("  AWS_ACCESS_KEY_ID      AWS access key (required)")
		fmt.Println("  AWS_SECRET_ACCESS_KEY  AWS secret key (required)")
		fmt.Println("  AWS_REGION             AWS region (required)")
		fmt.Println("  AWS_SESSION_TOKEN      Session token (optional)")
		os.Exit(1)
	}

	// Initialize report
	report := TestReport{
		Endpoint:  *endpoint,
		Bucket:    *bucket,
		StartTime: time.Now(),
	}

	if !*jsonOutput {
		fmt.Println("===========================================")
		fmt.Println("       S3 Validation Test Suite")
		fmt.Println("===========================================")
		fmt.Printf("Endpoint: %s\n", *endpoint)
		fmt.Printf("Bucket: %s\n", *bucket)
		fmt.Println()
	}

	// Load credentials
	creds := s3client.LoadFromEnv()
	if err := creds.Validate(); err != nil {
		if *jsonOutput {
			report.EndTime = time.Now()
			report.Duration = report.EndTime.Sub(report.StartTime)
			outputJSON(report)
		} else {
			fmt.Printf("Error: %v\n", err)
			fmt.Println("\nPlease set environment variables:")
			fmt.Println("  AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION")
		}
		os.Exit(1)
	}

	if !*jsonOutput {
		fmt.Printf("Region: %s\n", creds.AwsRegion)
		fmt.Printf("Using session token: %v\n", creds.AwsSessionToken != "")
		fmt.Println()
	}

	// Create client
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client, err := s3client.NewClient(ctx, creds, *endpoint)
	if err != nil {
		if *jsonOutput {
			report.EndTime = time.Now()
			report.Duration = report.EndTime.Sub(report.StartTime)
			outputJSON(report)
		} else {
			fmt.Printf("Error: Failed to create S3 client: %v\n", err)
		}
		os.Exit(1)
	}

	// Create test context
	tc := &TestContext{
		client:   client,
		bucket:   *bucket,
		endpoint: *endpoint,
		verbose:  *verbose,
	}

	if !*jsonOutput {
		fmt.Println("-------------------------------------------")
		fmt.Println("Running S3 Validation Tests...")
		fmt.Println("-------------------------------------------")
		fmt.Println()
	}

	// Define and run tests
	tests := []struct {
		name string
		fn   func(context.Context, *TestContext) TestResult
	}{
		{"PutThenGet", testPutThenGet},
		{"PutAtPrefixThenGet", testPutAtPrefixThenGet},
		{"StrongConsistency", testStrongConsistency},
		{"MultipartUpload", testMultipartUpload},
		{"PutObjectIfMatch", testPutObjectIfMatch},
		{"PutObjectIfNoneMatch", testPutObjectIfNoneMatch},
		{"ListObjects", testListObjects},
	}

	for _, test := range tests {
		result := test.fn(ctx, tc)
		result.Name = test.name
		report.Results = append(report.Results, result)
		report.TotalTests++

		if result.Passed {
			report.Passed++
		} else {
			report.Failed++
		}

		if !*jsonOutput {
			printTestResult(result, *verbose)
		}
	}

	report.EndTime = time.Now()
	report.Duration = report.EndTime.Sub(report.StartTime)

	// Output results
	if *jsonOutput {
		outputJSON(report)
	} else {
		fmt.Println("-------------------------------------------")
		fmt.Println("Test Summary")
		fmt.Println("-------------------------------------------")
		fmt.Printf("Passed: %d\n", report.Passed)
		fmt.Printf("Failed: %d\n", report.Failed)
		fmt.Printf("Total:  %d\n", report.TotalTests)
		fmt.Printf("Duration: %v\n", report.Duration.Round(time.Millisecond))
	}

	if report.Failed > 0 {
		os.Exit(1)
	}
}

func printTestResult(result TestResult, verbose bool) {
	status := "PASSED"
	if !result.Passed {
		status = "FAILED"
	}

	fmt.Printf("[%s] %s (%v)\n", status, result.Name, result.Duration.Round(time.Millisecond))

	if !result.Passed && result.Error != "" {
		fmt.Printf("  Error: %s\n", result.Error)
	}

	if verbose {
		for _, detail := range result.Details {
			fmt.Printf("  %s\n", detail)
		}
	}
	fmt.Println()
}

func outputJSON(report TestReport) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.Encode(report)
}

// =============================================================================
// Test Implementations
// =============================================================================

func testPutThenGet(ctx context.Context, tc *TestContext) TestResult {
	start := time.Now()
	result := TestResult{Details: []string{}}

	testData := "S3 Validation Test Data - Testing PutObject followed by GetObject."
	key := testObjectKey

	result.Details = append(result.Details, fmt.Sprintf("Key: %s", key))
	result.Details = append(result.Details, fmt.Sprintf("Data size: %d bytes", len(testData)))

	// PUT
	if err := tc.client.PutObject(ctx, tc.bucket, key, []byte(testData)); err != nil {
		result.Duration = time.Since(start)
		result.Error = fmt.Sprintf("PutObject failed: %v", err)
		return result
	}
	result.Details = append(result.Details, "PUT: success")

	// GET
	data, err := tc.client.GetObject(ctx, tc.bucket, key)
	if err != nil {
		result.Duration = time.Since(start)
		result.Error = fmt.Sprintf("GetObject failed: %v", err)
		return result
	}
	result.Details = append(result.Details, "GET: success")

	// Verify
	if string(data) != testData {
		result.Duration = time.Since(start)
		result.Error = "Content mismatch"
		return result
	}
	result.Details = append(result.Details, "Content verified")

	result.Duration = time.Since(start)
	result.Passed = true
	return result
}

func testPutAtPrefixThenGet(ctx context.Context, tc *TestContext) TestResult {
	start := time.Now()
	result := TestResult{Details: []string{}}

	testData := "S3 Validation Test Data - Testing PutObject with prefix followed by GetObject."
	key := testPrefixObjectKey

	result.Details = append(result.Details, fmt.Sprintf("Key: %s", key))
	result.Details = append(result.Details, fmt.Sprintf("Data size: %d bytes", len(testData)))

	// PUT
	if err := tc.client.PutObject(ctx, tc.bucket, key, []byte(testData)); err != nil {
		result.Duration = time.Since(start)
		result.Error = fmt.Sprintf("PutObject failed: %v", err)
		return result
	}
	result.Details = append(result.Details, "PUT: success")

	// GET
	data, err := tc.client.GetObject(ctx, tc.bucket, key)
	if err != nil {
		result.Duration = time.Since(start)
		result.Error = fmt.Sprintf("GetObject failed: %v", err)
		return result
	}
	result.Details = append(result.Details, "GET: success")

	// Verify
	if string(data) != testData {
		result.Duration = time.Since(start)
		result.Error = "Content mismatch"
		return result
	}
	result.Details = append(result.Details, "Content verified")

	result.Duration = time.Since(start)
	result.Passed = true
	return result
}

func testStrongConsistency(ctx context.Context, tc *TestContext) TestResult {
	const numIterations = 3
	const numReaders = 10
	const minSleepMs = 10
	const maxSleepMs = 50

	start := time.Now()
	result := TestResult{Details: []string{}}

	result.Details = append(result.Details, fmt.Sprintf("Iterations: %d", numIterations))
	result.Details = append(result.Details, fmt.Sprintf("Concurrent readers: %d", numReaders))
	result.Details = append(result.Details, fmt.Sprintf("Throttle: %d KB/s", throttleBytesPerSec/1024))

	for iteration := 1; iteration <= numIterations; iteration++ {
		iterResult := runConsistencyIteration(ctx, tc, iteration, numReaders, minSleepMs, maxSleepMs)
		result.Details = append(result.Details, fmt.Sprintf("Iteration %d: %s", iteration, iterResult))

		if iterResult != "passed" {
			result.Duration = time.Since(start)
			result.Error = fmt.Sprintf("Iteration %d failed: %s", iteration, iterResult)
			return result
		}
	}

	result.Duration = time.Since(start)
	result.Passed = true
	result.Details = append(result.Details, "All iterations passed - strong consistency verified")
	return result
}

func runConsistencyIteration(ctx context.Context, tc *TestContext, iteration, numReaders, minSleepMs, maxSleepMs int) string {
	// Alternate data
	var v1Data, v2Data []byte
	if iteration%2 == 1 {
		v1Data = assets.TestData2MB
		v2Data = assets.TestData2MBv2
	} else {
		v1Data = assets.TestData2MBv2
		v2Data = assets.TestData2MB
	}

	// Upload v1
	if err := tc.client.PutObject(ctx, tc.bucket, consistencyTestObjectKey, v1Data); err != nil {
		return fmt.Sprintf("failed to upload v1: %v", err)
	}

	etagV1, _, err := tc.client.HeadObject(ctx, tc.bucket, consistencyTestObjectKey)
	if err != nil {
		return fmt.Sprintf("failed to get v1 ETag: %v", err)
	}

	// Channel for observations
	observationChan := make(chan ETagObservation, 10000)

	var wg sync.WaitGroup
	var uploadErr error
	uploadComplete := make(chan struct{})

	// Start throttled upload
	wg.Add(1)
	go func() {
		defer wg.Done()
		uploadErr = tc.client.PutObjectThrottled(ctx, tc.bucket, consistencyTestObjectKey, v2Data, throttleBytesPerSec)
		close(uploadComplete)
	}()

	// Start readers
	monitorCtx, cancelMonitor := context.WithCancel(ctx)
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(readerID)*1000))

			for {
				select {
				case <-monitorCtx.Done():
					return
				default:
					sleepMs := minSleepMs + rng.Intn(maxSleepMs-minSleepMs+1)
					time.Sleep(time.Duration(sleepMs) * time.Millisecond)

					etag, _, err := tc.client.HeadObject(ctx, tc.bucket, consistencyTestObjectKey)
					if err != nil {
						continue
					}

					select {
					case observationChan <- ETagObservation{Timestamp: time.Now(), ETag: etag, ReaderID: readerID}:
					case <-monitorCtx.Done():
						return
					}
				}
			}
		}(i)
	}

	<-uploadComplete
	time.Sleep(300 * time.Millisecond)
	cancelMonitor()
	wg.Wait()
	close(observationChan)

	if uploadErr != nil {
		return fmt.Sprintf("upload failed: %v", uploadErr)
	}

	// Collect and analyze
	var observations []ETagObservation
	for obs := range observationChan {
		observations = append(observations, obs)
	}
	sort.Slice(observations, func(i, j int) bool {
		return observations[i].Timestamp.Before(observations[j].Timestamp)
	})

	// Find v2 ETag
	var etagV2 string
	for _, obs := range observations {
		if obs.ETag != etagV1 {
			etagV2 = obs.ETag
			break
		}
	}

	// Count and check ordering
	etagCounts := make(map[string]int)
	var firstV2Seen bool
	var v1AfterV2 int
	for _, obs := range observations {
		etagCounts[obs.ETag]++
		if obs.ETag == etagV2 {
			firstV2Seen = true
		} else if obs.ETag == etagV1 && firstV2Seen {
			v1AfterV2++
		}
	}

	if len(etagCounts) > 2 {
		return fmt.Sprintf("too many ETags: %d", len(etagCounts))
	}

	if etagV2 == "" {
		return "v2 never observed"
	}

	if v1AfterV2 > 0 {
		return fmt.Sprintf("v1 seen %d times after v2 (consistency violation)", v1AfterV2)
	}

	return "passed"
}

func testMultipartUpload(ctx context.Context, tc *TestContext) TestResult {
	start := time.Now()
	result := TestResult{Details: []string{}}

	const minPartSize = 5 * 1024 * 1024

	part1Marker := "=== PART 1: THE BEGINNING ==="
	part2Marker := "=== PART 2: THE MIDDLE ==="
	part3Marker := "=== PART 3: THE END ==="

	part1Content := make([]byte, minPartSize)
	copy(part1Content, part1Marker)
	for i := len(part1Marker); i < minPartSize; i++ {
		part1Content[i] = '1'
	}

	part2Content := make([]byte, minPartSize)
	copy(part2Content, part2Marker)
	for i := len(part2Marker); i < minPartSize; i++ {
		part2Content[i] = '2'
	}

	part3Content := []byte(part3Marker + " - Final small part.")

	expectedContent := make([]byte, 0, len(part1Content)+len(part2Content)+len(part3Content))
	expectedContent = append(expectedContent, part1Content...)
	expectedContent = append(expectedContent, part2Content...)
	expectedContent = append(expectedContent, part3Content...)

	result.Details = append(result.Details, fmt.Sprintf("Total size: %d bytes (~10MB)", len(expectedContent)))
	result.Details = append(result.Details, "Upload order: Part 3, Part 1, Part 2 (wrong order, correct numbers)")

	// Upload in wrong order
	parts := []s3client.MultipartUploadPart{
		{PartNumber: 3, Data: part3Content},
		{PartNumber: 1, Data: part1Content},
		{PartNumber: 2, Data: part2Content},
	}

	if err := tc.client.MultipartUpload(ctx, tc.bucket, multipartTestObjectKey, parts); err != nil {
		result.Duration = time.Since(start)
		result.Error = fmt.Sprintf("Multipart upload failed: %v", err)
		return result
	}
	result.Details = append(result.Details, "Upload: success")

	// Verify
	data, err := tc.client.GetObject(ctx, tc.bucket, multipartTestObjectKey)
	if err != nil {
		result.Duration = time.Since(start)
		result.Error = fmt.Sprintf("GetObject failed: %v", err)
		return result
	}

	if !bytes.Equal(data, expectedContent) {
		result.Duration = time.Since(start)
		result.Error = "Content mismatch"
		return result
	}

	// Check markers
	if string(data[0:len(part1Marker)]) != part1Marker ||
		string(data[len(part1Content):len(part1Content)+len(part2Marker)]) != part2Marker ||
		string(data[len(part1Content)+len(part2Content):len(part1Content)+len(part2Content)+len(part3Marker)]) != part3Marker {
		result.Duration = time.Since(start)
		result.Error = "Part markers not in correct positions"
		return result
	}

	result.Details = append(result.Details, "All part markers verified at correct positions")
	result.Duration = time.Since(start)
	result.Passed = true
	return result
}

func testPutObjectIfMatch(ctx context.Context, tc *TestContext) TestResult {
	start := time.Now()
	result := TestResult{Details: []string{}}

	initialContent := "Initial content v1"
	updatedContent := "Updated content v2"

	// Create initial
	if err := tc.client.PutObject(ctx, tc.bucket, conditionalTestObjectKey, []byte(initialContent)); err != nil {
		result.Duration = time.Since(start)
		result.Error = fmt.Sprintf("Failed to create initial object: %v", err)
		return result
	}

	etag, _, err := tc.client.HeadObject(ctx, tc.bucket, conditionalTestObjectKey)
	if err != nil {
		result.Duration = time.Since(start)
		result.Error = fmt.Sprintf("Failed to get ETag: %v", err)
		return result
	}
	result.Details = append(result.Details, fmt.Sprintf("Initial ETag: %s", etag))

	// Test wrong ETag
	wrongETag := "\"wrongetag123\""
	err = tc.client.PutObjectIfMatch(ctx, tc.bucket, conditionalTestObjectKey, []byte(updatedContent), wrongETag)
	if err != s3client.ErrPreconditionFailed {
		result.Duration = time.Since(start)
		result.Error = fmt.Sprintf("Expected precondition failed, got: %v", err)
		return result
	}
	result.Details = append(result.Details, "Wrong ETag correctly rejected")

	// Verify unchanged
	data, _ := tc.client.GetObject(ctx, tc.bucket, conditionalTestObjectKey)
	if string(data) != initialContent {
		result.Duration = time.Since(start)
		result.Error = "Content changed after failed update"
		return result
	}

	// Test correct ETag
	if err := tc.client.PutObjectIfMatch(ctx, tc.bucket, conditionalTestObjectKey, []byte(updatedContent), etag); err != nil {
		result.Duration = time.Since(start)
		result.Error = fmt.Sprintf("Update with correct ETag failed: %v", err)
		return result
	}
	result.Details = append(result.Details, "Correct ETag accepted")

	// Verify changed
	data, _ = tc.client.GetObject(ctx, tc.bucket, conditionalTestObjectKey)
	if string(data) != updatedContent {
		result.Duration = time.Since(start)
		result.Error = "Content not updated"
		return result
	}
	result.Details = append(result.Details, "Content correctly updated")

	result.Duration = time.Since(start)
	result.Passed = true
	return result
}

func testPutObjectIfNoneMatch(ctx context.Context, tc *TestContext) TestResult {
	start := time.Now()
	result := TestResult{Details: []string{}}

	testKey := fmt.Sprintf("conditional-create-test-%d.txt", time.Now().UnixNano())
	content := "Original content"
	newContent := "New content"

	result.Details = append(result.Details, fmt.Sprintf("Key: %s", testKey))

	// Cleanup
	_ = tc.client.DeleteObject(ctx, tc.bucket, testKey)

	// Verify not exists
	exists, err := tc.client.ObjectExists(ctx, tc.bucket, testKey)
	if err != nil {
		result.Duration = time.Since(start)
		result.Error = fmt.Sprintf("Failed to check existence: %v", err)
		return result
	}
	if exists {
		result.Duration = time.Since(start)
		result.Error = "Object exists after delete"
		return result
	}

	// Create (should succeed)
	if err := tc.client.PutObjectIfNoneMatch(ctx, tc.bucket, testKey, []byte(content)); err != nil {
		result.Duration = time.Since(start)
		result.Error = fmt.Sprintf("Create failed: %v", err)
		return result
	}
	result.Details = append(result.Details, "Create succeeded (object didn't exist)")

	// Create again (should fail)
	err = tc.client.PutObjectIfNoneMatch(ctx, tc.bucket, testKey, []byte(newContent))
	if err != s3client.ErrObjectAlreadyExists {
		result.Duration = time.Since(start)
		result.Error = fmt.Sprintf("Expected object already exists, got: %v", err)
		return result
	}
	result.Details = append(result.Details, "Second create correctly rejected")

	// Verify original content
	data, _ := tc.client.GetObject(ctx, tc.bucket, testKey)
	if string(data) != content {
		result.Duration = time.Since(start)
		result.Error = "Original content changed"
		return result
	}

	// Cleanup
	_ = tc.client.DeleteObject(ctx, tc.bucket, testKey)

	result.Duration = time.Since(start)
	result.Passed = true
	return result
}

func testListObjects(ctx context.Context, tc *TestContext) TestResult {
	start := time.Now()
	result := TestResult{Details: []string{}}

	// Create 5 test files with unique content
	type testFile struct {
		key     string
		content string
		etag    string
	}

	testFiles := []testFile{
		{key: listObjectsTestPrefix + "file1.txt", content: "Content of file 1"},
		{key: listObjectsTestPrefix + "file2.txt", content: "Content of file 2 - slightly longer"},
		{key: listObjectsTestPrefix + "file3.txt", content: "File 3"},
		{key: listObjectsTestPrefix + "file4.txt", content: "Fourth file content here"},
		{key: listObjectsTestPrefix + "file5.txt", content: "Fifth and final test file"},
	}

	result.Details = append(result.Details, fmt.Sprintf("Prefix: %s", listObjectsTestPrefix))
	result.Details = append(result.Details, fmt.Sprintf("File count: %d", len(testFiles)))

	// Upload files and record ETags
	for i := range testFiles {
		err := tc.client.PutObject(ctx, tc.bucket, testFiles[i].key, []byte(testFiles[i].content))
		if err != nil {
			result.Duration = time.Since(start)
			result.Error = fmt.Sprintf("Failed to upload %s: %v", testFiles[i].key, err)
			return result
		}

		// Get ETag via HeadObject
		etag, _, err := tc.client.HeadObject(ctx, tc.bucket, testFiles[i].key)
		if err != nil {
			result.Duration = time.Since(start)
			result.Error = fmt.Sprintf("Failed to get ETag for %s: %v", testFiles[i].key, err)
			return result
		}
		testFiles[i].etag = etag
	}
	result.Details = append(result.Details, "PUT: success (5 files)")

	// List objects
	objects, err := tc.client.ListObjects(ctx, tc.bucket, listObjectsTestPrefix)
	if err != nil {
		result.Duration = time.Since(start)
		result.Error = fmt.Sprintf("ListObjects failed: %v", err)
		return result
	}
	result.Details = append(result.Details, fmt.Sprintf("LIST: success (%d objects)", len(objects)))

	// Verify count
	if len(objects) != len(testFiles) {
		result.Duration = time.Since(start)
		result.Error = fmt.Sprintf("Expected %d objects, got %d", len(testFiles), len(objects))
		return result
	}

	// Build map of listed objects for verification
	listedObjects := make(map[string]s3client.ObjectInfo)
	for _, obj := range objects {
		listedObjects[obj.Key] = obj
	}

	// Verify each file
	for _, tf := range testFiles {
		obj, exists := listedObjects[tf.key]
		if !exists {
			result.Duration = time.Since(start)
			result.Error = fmt.Sprintf("File %s not found in listing", tf.key)
			return result
		}

		// Verify ETag
		if obj.ETag != tf.etag {
			result.Duration = time.Since(start)
			result.Error = fmt.Sprintf("ETag mismatch for %s: expected %s, got %s", tf.key, tf.etag, obj.ETag)
			return result
		}

		// Verify size
		expectedSize := int64(len(tf.content))
		if obj.Size != expectedSize {
			result.Duration = time.Since(start)
			result.Error = fmt.Sprintf("Size mismatch for %s: expected %d, got %d", tf.key, expectedSize, obj.Size)
			return result
		}
	}
	result.Details = append(result.Details, "Keys, ETags, sizes verified")

	// Cleanup
	for _, tf := range testFiles {
		_ = tc.client.DeleteObject(ctx, tc.bucket, tf.key)
	}

	result.Duration = time.Since(start)
	result.Passed = true
	return result
}
