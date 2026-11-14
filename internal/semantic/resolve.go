package semantic

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

//go:embed compiler.cjs
var compiler string

type Position struct {
	File   string `json:"file"`
	Line   uint   `json:"line"`
	Column uint   `json:"column"`
}

type Edge struct {
	Site   Position `json:"site"`
	Target Position `json:"target"`
}

type Report struct {
	Basis           string `json:"basis"`
	Status          string `json:"status"`
	Message         string `json:"message,omitempty"`
	CompilerVersion string `json:"compilerVersion,omitempty"`
	Programs        int    `json:"programs"`
	Diagnostics     int    `json:"diagnostics"`
	Calls           []Edge `json:"calls,omitempty"`
	Imports         []Edge `json:"imports,omitempty"`
}

func Resolve(ctx context.Context, root string, files []string) Report {
	bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	result := Report{Basis: "typescript-compiler", Status: "fallback"}
	input, err := json.Marshal(files)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	command := exec.CommandContext(bounded, "node", "-e", compiler, root)
	command.Dir = root
	command.Stdin = bytes.NewReader(input)
	command.WaitDelay = time.Second
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		result.Message = strings.TrimSpace(stderr.String())
		if result.Message == "" {
			result.Message = err.Error()
		}
		return result
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return Report{Basis: result.Basis, Status: "fallback", Message: fmt.Sprintf("invalid TypeScript result: %v", err)}
	}
	return result
}
