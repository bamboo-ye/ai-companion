package document

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type PythonParser struct {
	Executable string
	ModulePath string
}

func (p PythonParser) Parse(ctx context.Context, mediaType string, data []byte) (ParseResult, error) {
	executable := strings.TrimSpace(p.Executable)
	if executable == "" {
		executable = "python3"
	}
	command := exec.CommandContext(ctx, executable, "-m", "ai_companion_worker.document_parser", "--media-type", mediaType)
	command.Stdin = bytes.NewReader(data)
	command.Env = os.Environ()
	if path := strings.TrimSpace(p.ModulePath); path != "" {
		command.Env = append(command.Env, "PYTHONPATH="+path)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if len(message) > 500 {
			message = message[:500]
		}
		return ParseResult{}, fmt.Errorf("python parser: %w: %s", err, message)
	}
	var result ParseResult
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return ParseResult{}, fmt.Errorf("decode parser result: %w", err)
	}
	if result.ParserVersion == "" || len(result.Pages) == 0 || len(result.Chunks) == 0 ||
		stringValue(result.SourceIR["version"]) != SourceIRVersion {
		return ParseResult{}, fmt.Errorf("parser returned incomplete result")
	}
	return result, nil
}
