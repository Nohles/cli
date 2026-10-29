package benchfixture

import (
	"archive/zip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryComicFixture(t *testing.T) {
	root := filepath.Join(t.TempDir(), "library")
	names, err := DirectoryComics(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 100 {
		t.Fatalf("chapters: %d", len(names))
	}
	hashes := map[[32]byte]bool{}
	for i, name := range names {
		if name != fmt.Sprintf("Chapter %03d.cbz", i+1) {
			t.Fatalf("chapter order: %s", name)
		}
		path := filepath.Join(root, "Series", name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		if hashes[hash] {
			t.Fatal("chapter archives must be distinct")
		}
		hashes[hash] = true
		z, err := zip.OpenReader(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(z.File) != 25 {
			z.Close()
			t.Fatalf("expected 24 pages and ComicInfo.xml, got %d", len(z.File))
		}
		r, err := z.File[1].Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err = io.ReadAll(r)
		r.Close()
		z.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
			t.Fatal("invalid page PNG")
		}
	}
	if _, err := DirectoryComics(root); err == nil {
		t.Fatal("must refuse an existing fixture library")
	}
	if _, err := os.Stat(filepath.Join(root, "Series", names[0])); err != nil {
		t.Fatal("existing media was modified")
	}
}
