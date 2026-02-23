package assets

import (
	_ "embed"
)

// TestData2MB contains 2MB of random data for bandwidth testing.
// This data is embedded into the binary at compile time.
//
//go:embed testdata_2mb.bin
var TestData2MB []byte

// TestData2MBv2 contains a second 2MB of random data for replace/overwrite testing.
// This data is different from TestData2MB and is used to test uploading a new version.
//
//go:embed testdata_2mb_v2.bin
var TestData2MBv2 []byte
