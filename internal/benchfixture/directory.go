package benchfixture

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

const DirectoryComicChapters = 100
const DirectoryComicPages = 24

// DirectoryComics creates a new, deterministic library containing Series with
// 100 distinct CBZ chapters. It refuses to overwrite an existing directory.
func DirectoryComics(dir string) (names []string, err error) {
	if err = os.Mkdir(dir, 0700); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	series := filepath.Join(dir, "Series")
	if err = os.Mkdir(series, 0700); err != nil {
		return nil, err
	}
	img := image.NewNRGBA(image.Rect(0, 0, 256, 384))
	for y := range 384 {
		for x := range 256 {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), uint8(x * y / 17), 255})
		}
	}
	var page bytes.Buffer
	if err = png.Encode(&page, img); err != nil {
		return nil, err
	}
	for chapter := 1; chapter <= DirectoryComicChapters; chapter++ {
		name := fmt.Sprintf("Chapter %03d.cbz", chapter)
		err = withZIP(filepath.Join(series, name), func(z *zip.Writer) error {
			info := fmt.Sprintf("<ComicInfo><Title>Chapter %03d</Title><Number>%03d</Number><PageCount>%d</PageCount></ComicInfo>", chapter, chapter, DirectoryComicPages)
			if err := entry(z, "ComicInfo.xml", zip.Deflate, []byte(info)); err != nil {
				return err
			}
			for p := range DirectoryComicPages {
				if err := entry(z, fmt.Sprintf("%04d.png", p), zip.Store, page.Bytes()); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}
