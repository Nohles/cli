package cli

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/nohles/go-toolkit/pkg/inspector"
	"github.com/spf13/cobra"
)

const maxInspectorHeaderBytes = 1 << 20
const maxInspectorCoverBytes = 64 << 20

type inspectorResponse struct {
	Metadata map[string]any `json:"metadata,omitempty"`
	Cover    *struct {
		MIMEType string `json:"mimeType"`
		Locator  string `json:"locator"`
	} `json:"cover,omitempty"`
	CoverLength       int                   `json:"coverLength"`
	SourceFingerprint string                `json:"sourceFingerprint,omitempty"`
	ExtractorVersion  string                `json:"extractorVersion"`
	Diagnostics       inspector.Diagnostics `json:"diagnostics"`
	Status            string                `json:"status"`
	Error             string                `json:"error,omitempty"`
}

var inspectWorkerCmd = &cobra.Command{
	Use:   "inspect-worker",
	Short: "Run the framed publication inspection worker",
	Args:  cobra.NoArgs,
	RunE:  func(_ *cobra.Command, _ []string) error { return runInspectWorker(os.Stdin, os.Stdout) },
}

func init() { rootCmd.AddCommand(inspectWorkerCmd) }

func runInspectWorker(input io.Reader, output io.Writer) error {
	r := bufio.NewReader(input)
	w := bufio.NewWriter(output)
	for {
		var size uint32
		if err := binary.Read(r, binary.BigEndian, &size); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if size == 0 || size > maxInspectorHeaderBytes {
			return fmt.Errorf("invalid inspector request header length %d", size)
		}
		header := make([]byte, size)
		if _, err := io.ReadFull(r, header); err != nil {
			return err
		}
		var req inspector.Request
		if err := json.Unmarshal(header, &req); err != nil {
			return err
		}
		result, inspectErr := inspector.Inspect(req)
		response := inspectorResponse{Metadata: result.Metadata, SourceFingerprint: result.SourceFingerprint, ExtractorVersion: result.ExtractorVersion, Diagnostics: result.Diagnostics, Status: "ready"}
		var cover []byte
		if inspectErr != nil {
			response.Status = "unavailable"
			response.Error = inspectErr.Error()
			if errors.Is(inspectErr, inspector.ErrUnsupported) {
				response.Status = "unsupported"
			}
		} else if result.Cover != nil {
			response.Cover = &struct {
				MIMEType string `json:"mimeType"`
				Locator  string `json:"locator"`
			}{result.Cover.MIMEType, result.Cover.Locator}
			cover = result.Cover.Bytes
			response.CoverLength = len(cover)
		}
		if len(cover) > maxInspectorCoverBytes {
			response.Status = "unavailable"
			response.Error = "inspector cover payload is too large"
			response.Cover = nil
			response.CoverLength = 0
			cover = nil
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return err
		}
		if len(encoded) > maxInspectorHeaderBytes {
			return errors.New("inspector response header is too large")
		}
		if err := binary.Write(w, binary.BigEndian, uint32(len(encoded))); err != nil {
			return err
		}
		if _, err := w.Write(encoded); err != nil {
			return err
		}
		if _, err := w.Write(cover); err != nil {
			return err
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
}
