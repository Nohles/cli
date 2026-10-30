package benchfixture

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Existing validates media in place. It never generates or copies files, so a
// benchmark directed at a mounted library actually reads that filesystem.
// Names are exactly one EPUB, one comic and one audio file, in that order.
func Existing(dir, names string) (string, []string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() {
		return "", nil, fmt.Errorf("library is not a directory: %s", abs)
	}
	files := strings.Split(names, ",")
	if len(files) != 3 {
		return "", nil, fmt.Errorf("files must name three basenames: EPUB, comic, audio")
	}
	seen := make(map[string]bool)
	for _, name := range files {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\:`) || seen[name] {
			return "", nil, fmt.Errorf("invalid or duplicate fixture basename: %q", name)
		}
		info, err := os.Lstat(filepath.Join(abs, name))
		if err != nil {
			return "", nil, err
		}
		if !info.Mode().IsRegular() {
			return "", nil, fmt.Errorf("fixture must be a regular file: %s", name)
		}
		seen[name] = true
	}
	return abs, files, nil
}
