// Package benchfixture generates deterministic, non-private publication fixtures.
package benchfixture

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Library writes an EPUB with 120 chapters, a 240-page CBZ, and 60 seconds of
// mono PCM WAV. The audio is valid but does not exercise embedded M4B chapters.
func Library(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := writeEPUB(filepath.Join(dir, "book.epub")); err != nil {
		return nil, err
	}
	if err := writeComic(filepath.Join(dir, "comic.cbz")); err != nil {
		return nil, err
	}
	if err := writeWAV(filepath.Join(dir, "audio.wav")); err != nil {
		return nil, err
	}
	return []string{"book.epub", "comic.cbz", "audio.wav"}, nil
}

func withZIP(path string, build func(*zip.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	z := zip.NewWriter(f)
	if err := build(z); err != nil {
		z.Close()
		return err
	}
	return z.Close()
}
func entry(z *zip.Writer, name string, method uint16, data []byte) error {
	w, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: method})
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
func writeEPUB(path string) error {
	return withZIP(path, func(z *zip.Writer) error {
		if err := entry(z, "mimetype", zip.Store, []byte("application/epub+zip")); err != nil {
			return err
		}
		if err := entry(z, "META-INF/container.xml", zip.Deflate, []byte(`<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="EPUB/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`)); err != nil {
			return err
		}
		var items, spine, toc strings.Builder
		for i := range 120 {
			name := fmt.Sprintf("chapter-%03d.xhtml", i)
			fmt.Fprintf(&items, `<item id="c%d" href="%s" media-type="application/xhtml+xml"/>`, i, name)
			fmt.Fprintf(&spine, `<itemref idref="c%d"/>`, i)
			fmt.Fprintf(&toc, `<li><a href="%s">Chapter %d</a></li>`, name, i+1)
			var body strings.Builder
			fmt.Fprintf(&body, `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Chapter %d</title></head><body>`, i+1)
			// Varied text avoids measuring an unrealistically tiny compressed archive.
			state := uint32(i + 1)
			for range 700 {
				state ^= state << 13
				state ^= state >> 17
				state ^= state << 5
				fmt.Fprintf(&body, "<p>Reader benchmark paragraph %08x: The quick brown fox reads a book.</p>", state)
			}
			body.WriteString("</body></html>")
			if err := entry(z, "EPUB/"+name, zip.Deflate, []byte(body.String())); err != nil {
				return err
			}
		}
		opf := `<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="uid">urn:reader:benchmark</dc:identifier><dc:title>Reader benchmark</dc:title><dc:language>en</dc:language></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>` + items.String() + `</manifest><spine>` + spine.String() + `</spine></package>`
		if err := entry(z, "EPUB/package.opf", zip.Deflate, []byte(opf)); err != nil {
			return err
		}
		return entry(z, "EPUB/nav.xhtml", zip.Deflate, []byte(`<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>Contents</title></head><body><nav epub:type="toc"><ol>`+toc.String()+`</ol></nav></body></html>`))
	})
}
func writeComic(path string) error {
	img := image.NewNRGBA(image.Rect(0, 0, 256, 384))
	for y := 0; y < 384; y++ {
		for x := 0; x < 256; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), uint8((x * y) / 17), 255})
		}
	}
	var page bytes.Buffer
	if err := png.Encode(&page, img); err != nil {
		return err
	}
	return withZIP(path, func(z *zip.Writer) error {
		for i := range 240 {
			if err := entry(z, fmt.Sprintf("%04d.png", i), zip.Store, page.Bytes()); err != nil {
				return err
			}
		}
		return nil
	})
}
func writeWAV(path string) error {
	const rate = 22050
	const size = rate * 60 * 2
	var header [44]byte
	copy(header[:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], size+36)
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 1)
	binary.LittleEndian.PutUint32(header[24:], rate)
	binary.LittleEndian.PutUint32(header[28:], rate*2)
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], size)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(header[:]); err != nil {
		return err
	}
	samples := make([]byte, rate*2)
	for i := range rate {
		binary.LittleEndian.PutUint16(samples[i*2:], uint16(int16(8000*math.Sin(2*math.Pi*440*float64(i)/rate))))
	}
	for range 60 {
		if _, err = io.Copy(f, bytes.NewReader(samples)); err != nil {
			return err
		}
	}
	return nil
}
