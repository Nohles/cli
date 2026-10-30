package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/readium/cli/internal/benchfixture"
)

// TestCompareColdOpens is opt-in qualification for storage-history-sensitive
// cold opens. Normal correctness suites skip it. It alternates two binaries
// instead of discarding slow samples from the full workload run.
func TestCompareColdOpens(t *testing.T) {
	before, after := os.Getenv("READIUM_COMPARE_BEFORE"), os.Getenv("READIUM_COMPARE_AFTER")
	if before == "" && after == "" {
		t.Skip("set READIUM_COMPARE_BEFORE/AFTER/LIBRARY/OUTPUT for qualification")
	}
	library, names := os.Getenv("READIUM_COMPARE_LIBRARY"), os.Getenv("READIUM_COMPARE_FILES")
	output := os.Getenv("READIUM_COMPARE_OUTPUT")
	if before == "" || after == "" || library == "" || output == "" {
		t.Fatal("before, after, library and output are required")
	}
	if names == "" {
		names = "external-0.epub,comic.cbz,external-2.m4b"
	}
	library, files, err := benchfixture.Existing(library, names)
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	type record struct {
		Pair    int         `json:"pair"`
		Variant string      `json:"variant"`
		Media   string      `json:"media"`
		Sample  measurement `json:"sample"`
	}
	report := struct {
		Before  fixture  `json:"before_binary"`
		After   fixture  `json:"after_binary"`
		Library string   `json:"library"`
		Records []record `json:"records"`
	}{Library: library}
	report.Before, err = digest(before)
	if err != nil {
		t.Fatal(err)
	}
	report.After, err = digest(after)
	if err != nil {
		t.Fatal(err)
	}
	for pair := range 30 {
		order := []string{"before", "after"}
		if pair%2 != 0 {
			order = []string{"after", "before"}
		}
		for _, variant := range order {
			binary := before
			if variant == "after" {
				binary = after
			}
			p, err := start(binary, library, filepath.Join(t.TempDir(), variant))
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range files {
				_, sample, err := p.get("/webpub/"+base64.RawURLEncoding.EncodeToString([]byte(name))+"/manifest.json", "")
				if err != nil {
					p.close()
					t.Fatal(err)
				}
				report.Records = append(report.Records, record{pair, variant, name, sample})
			}
			p.close()
		}
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		t.Fatal(err)
	}
}
