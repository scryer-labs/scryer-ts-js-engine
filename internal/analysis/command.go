package analysis

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/scryer-labs/scryer-ts-js-engine/internal/process"
)

// Command keeps test subprocess cancellation aligned with the ABI request.
func Command(ctx context.Context, directory string, extra map[string]string, program string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = directory
	cmd.WaitDelay = 3 * time.Second
	process.ConfigureCancellation(cmd)
	env := map[string]string{}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	for key, value := range extra {
		env[key] = value
	}
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		message := stderr.String()
		if len(message) > 16000 {
			message = message[len(message)-16000:]
		}
		if len(output) > 16000 {
			output = output[len(output)-16000:]
		}
		return output, fmt.Errorf("%s failed: %w\n%s\n%s", program, err, message, output)
	}
	return output, nil
}

func Hash(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func FinalizeFiles(files map[string]*CoverageFile) []CoverageFile {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]CoverageFile, 0, len(names))
	for _, name := range names {
		file := files[name]
		regions := CoverageCount{Count: uint64(len(file.Regions))}
		lines := map[int]bool{}
		for _, region := range file.Regions {
			if region.Count > 0 {
				regions.Covered++
			}
			end := region.End.Line
			if region.End.Column == 1 {
				end--
			}
			for line := region.Start.Line; line <= end; line++ {
				lines[line] = lines[line] || region.Count > 0
			}
		}
		lineCounts := CoverageCount{Count: uint64(len(lines))}
		for _, hit := range lines {
			if hit {
				lineCounts.Covered++
			}
		}
		file.Summary.Regions = &regions
		file.Summary.Lines = &lineCounts
		result = append(result, *file)
	}
	return result
}
