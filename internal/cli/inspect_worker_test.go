package cli

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/nohles/go-toolkit/pkg/inspector"
	"github.com/stretchr/testify/require"
)

func TestInspectWorkerUsesLengthPrefixedHeaders(t *testing.T) {
	request, err := json.Marshal(inspector.Request{
		FilePath:        "/definitely/missing.epub",
		MediaKind:       "books",
		IncludeMetadata: true,
	})
	require.NoError(t, err)
	input := new(bytes.Buffer)
	require.NoError(t, binary.Write(input, binary.BigEndian, uint32(len(request))))
	_, err = input.Write(request)
	require.NoError(t, err)

	output := new(bytes.Buffer)
	require.NoError(t, runInspectWorker(input, output))
	var headerLength uint32
	require.NoError(t, binary.Read(output, binary.BigEndian, &headerLength))
	header := make([]byte, headerLength)
	_, err = output.Read(header)
	require.NoError(t, err)
	var response inspectorResponse
	require.NoError(t, json.Unmarshal(header, &response))
	require.Equal(t, "unavailable", response.Status)
	require.Zero(t, response.CoverLength)
}
