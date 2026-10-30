package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/readium/cli/internal/benchfixture"
)

// prepareLibrary owns only a newly-created directory. It never overwrites a
// pre-existing library, and only removes its own directory if preparation fails.
func prepareLibrary(dir, epub, audio string) (files []string, err error) {
	if err = os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	files, err = benchfixture.Library(dir)
	if err != nil {
		return nil, err
	}
	for i, source := range map[int]string{0: epub, 2: audio} {
		if source == "" {
			continue
		}
		name := fmt.Sprintf("external-%d%s", i, strings.ToLower(filepath.Ext(source)))
		if err = copyFile(source, filepath.Join(dir, name)); err != nil {
			return nil, err
		}
		files[i] = name
	}
	return files, nil
}
