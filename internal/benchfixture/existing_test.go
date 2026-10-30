package benchfixture

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExistingRejectsTraversalDuplicatesAndMissingFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"book.epub", "comic.cbz", "audio.wav"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, names := range []string{"../book.epub,comic.cbz,audio.wav", `C:\book.epub,comic.cbz,audio.wav`, "book.epub,book.epub,audio.wav", "missing.epub,comic.cbz,audio.wav", "book.epub,comic.cbz"} {
		if _, _, err := Existing(dir, names); err == nil {
			t.Fatalf("accepted invalid selection %q", names)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "directory.epub"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Existing(dir, "directory.epub,comic.cbz,audio.wav"); err == nil {
		t.Fatal("accepted a directory as media")
	}
}
