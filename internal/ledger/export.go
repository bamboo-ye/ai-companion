package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var ErrExporterUnavailable = errors.New("ledger exporter unavailable")

type ExportPayload struct {
	Month       string    `json:"month"`
	Currency    string    `json:"currency"`
	Timezone    string    `json:"timezone"`
	GeneratedAt time.Time `json:"generated_at"`
	Entries     []Entry   `json:"entries"`
}

type Exporter interface {
	Export(context.Context, ExportPayload) ([]byte, error)
}

type ArtifactToolExporter struct {
	Executable string
	ScriptPath string
	Timeout    time.Duration
}

func (e ArtifactToolExporter) Export(ctx context.Context, payload ExportPayload) ([]byte, error) {
	if strings.TrimSpace(e.Executable) == "" || strings.TrimSpace(e.ScriptPath) == "" {
		return nil, ErrExporterUnavailable
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "ai-companion-ledger-export-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	outputPath := filepath.Join(dir, "ledger.xlsx")
	input, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, e.Executable, e.ScriptPath, outputPath)
	command.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	command.Stderr = &limitedWriter{writer: &stderr, remaining: 4096}
	if err = command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: export timed out", ErrExporterUnavailable)
		}
		return nil, fmt.Errorf("%w: %s", ErrExporterUnavailable, strings.TrimSpace(stderr.String()))
	}
	file, err := os.Open(outputPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (50<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > 50<<20 {
		return nil, fmt.Errorf("%w: invalid workbook size", ErrExporterUnavailable)
	}
	return data, nil
}

type limitedWriter struct {
	writer    io.Writer
	remaining int
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	original := len(data)
	if w.remaining <= 0 {
		return original, nil
	}
	if len(data) > w.remaining {
		data = data[:w.remaining]
	}
	_, err := w.writer.Write(data)
	w.remaining -= len(data)
	return original, err
}
