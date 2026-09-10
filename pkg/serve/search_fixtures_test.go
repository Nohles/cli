package serve

import (
	"archive/zip"
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// buildMiniEPUB assembles a minimal two-chapter EPUB whose searchable text is
// exactly the given chapter bodies, for cache-invalidation assertions.
func buildMiniEPUB(t *testing.T, ch1, ch2 string) []byte {
	t.Helper()
	chapter := func(body string) string {
		return `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>c</title></head>
<body><p>` + body + `</p></body></html>`
	}
	container := `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
<rootfiles><rootfile full-path="EPUB/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`

	pkg := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:identifier id="uid">urn:uuid:mini-fixture</dc:identifier>
<dc:title>mini</dc:title><dc:language>en</dc:language>
<dc:creator>a</dc:creator></metadata>
<manifest>
<item id="ch1" href="ch01.xhtml" media-type="application/xhtml+xml"/>
<item id="ch2" href="ch02.xhtml" media-type="application/xhtml+xml"/>
<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
</manifest>
<spine><itemref idref="ch1"/><itemref idref="ch2"/><itemref idref="nav"/></spine></package>`

	nav := `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>nav</title></head><body><nav epub:type="toc"><ol>
<li><a href="ch01.xhtml">One</a></li><li><a href="ch02.xhtml">Two</a></li>
</ol></nav></body></html>`

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string]string{
		"mimetype":            "application/epub+zip",
		"META-INF/container.xml": container,
		"EPUB/package.opf":    pkg,
		"EPUB/nav.xhtml":      nav,
		"EPUB/ch01.xhtml":     chapter(ch1),
		"EPUB/ch02.xhtml":     chapter(ch2),
	}
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}
