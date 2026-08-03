package wsbackend

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// EmbeddedBackend implements FileOperator, BashOperator, and DirLister
// using the local filesystem and os/exec.
//
// File mutations (Write, Edit) are serialised per resolved path via
// an internal mutex map. No two goroutines can mutate the same file
// concurrently; reads are lock-free.
type EmbeddedBackend struct {
	RootDir   string
	fileLocks map[string]*sync.Mutex
	locksMu   sync.Mutex
}

// NewEmbeddedBackend returns an EmbeddedBackend initialised with an
// empty file lock map. Must be used instead of direct struct literals
// so that the per-file mutex map is not nil.
func NewEmbeddedBackend(rootDir string) *EmbeddedBackend {
	expanded, err := expandHome(rootDir)
	if err != nil {
		log.Printf("[wsbackend] expandHome %q: %v", rootDir, err)
	} else if expanded != rootDir {
		log.Printf("[wsbackend] expandHome %q → %q", rootDir, expanded)
	}
	rootDir = expanded
	return &EmbeddedBackend{
		RootDir:   rootDir,
		fileLocks: make(map[string]*sync.Mutex),
	}
}

// fileLock returns a function that unlocks the per-file mutex for path.
func (b *EmbeddedBackend) fileLock(path string) func() {
	b.locksMu.Lock()
	mu, ok := b.fileLocks[path]
	if !ok {
		mu = &sync.Mutex{}
		b.fileLocks[path] = mu
	}
	b.locksMu.Unlock()

	mu.Lock()
	return mu.Unlock
}

// Read reads a file within the workspace root. offset is 1-indexed (0 = no
// offset), limit caps the number of lines returned (0 = no limit). The
// returned string contains only the requested slice.
func (b *EmbeddedBackend) Read(ctx context.Context, path string, offset, limit int) (string, error) {
	resolved, err := resolvePath(b.RootDir, path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	content := string(data)
	if offset <= 0 && limit <= 0 {
		return content, nil
	}
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if offset < 1 {
		offset = 1
	}
	if offset > len(lines) {
		return "", nil
	}
	lines = lines[offset-1:]
	if limit > 0 && len(lines) > limit {
		lines = lines[:limit]
	}
	return strings.Join(lines, "\n"), nil
}

func (b *EmbeddedBackend) Write(ctx context.Context, path, content string) error {
	resolved, err := resolvePath(b.RootDir, path)
	if err != nil {
		return err
	}

	unlock := b.fileLock(resolved)
	defer unlock()

	if err := os.MkdirAll(filepath.Dir(resolved), 0755); err != nil {
		return fmt.Errorf("create parent dirs: %w", err)
	}

	return os.WriteFile(resolved, []byte(content), 0644)
}

// Edit performs an atomic (read -> check -> write) replacement under the
// per-file lock. If the exact oldStr is not found, it falls back to a
// Unicode-normalised fuzzy match (curly quotes -> straight, em-dashes -> --).
func (b *EmbeddedBackend) Edit(ctx context.Context, path, oldStr, newStr string) error {
	resolved, err := resolvePath(b.RootDir, path)
	if err != nil {
		return err
	}

	unlock := b.fileLock(resolved)
	defer unlock()

	data, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Errorf("read file for edit: %w", err)
	}
	content := string(data)

	// Exact match.
	count := strings.Count(content, oldStr)
	if count == 1 {
		newContent := strings.Replace(content, oldStr, newStr, 1)
		return os.WriteFile(resolved, []byte(newContent), 0644)
	}
	if count > 1 {
		return fmt.Errorf("old_string appears %d times in %s - provide a more unique match", count, path)
	}

	// Fuzzy fallback.
	normContent := NormalizeQuotes(content)
	normOld := NormalizeQuotes(oldStr)
	normCount := strings.Count(normContent, normOld)
	if normCount == 0 {
		return fmt.Errorf("old_string not found in %s (exact and fuzzy)", path)
	}
	if normCount > 1 {
		return fmt.Errorf("old_string appears %d times in %s after fuzzy normalization - provide a more unique match", normCount, path)
	}

	idx := strings.Index(normContent, normOld)
	newContent := content[:idx] + newStr + content[idx+len(oldStr):]
	return os.WriteFile(resolved, []byte(newContent), 0644)
}

// List returns the contents of a directory as []DirEntry, sorted by name
// (directories first, then files). Implements [DirLister].
func (b *EmbeddedBackend) List(ctx context.Context, path string) ([]DirEntry, error) {
	resolved, err := resolvePath(b.RootDir, path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, fmt.Errorf("list directory: %w", err)
	}
	result := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		if e.Name() == "" {
			continue
		}
		result = append(result, DirEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].IsDir != result[j].IsDir {
			return result[i].IsDir
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// Remove deletes a file or directory at the given path relative to the root.
// If the path is a directory, it is removed recursively along with all contents.
func (b *EmbeddedBackend) Remove(ctx context.Context, path string) error {
	resolved, err := resolvePath(b.RootDir, path)
	if err != nil {
		return err
	}

	// Security: ensure the resolved path is within the workspace root.
	absRoot, _ := filepath.Abs(b.RootDir)
	absResolved, err := filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	}
	if !strings.HasPrefix(absResolved, absRoot) {
		return fmt.Errorf("path %q escapes workspace root", path)
	}

	// Check if the path exists.
	_, err = os.Stat(absResolved)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("path %q not found", path)
		}
		return fmt.Errorf("stat %q: %w", path, err)
	}

	if err := os.RemoveAll(absResolved); err != nil {
		return fmt.Errorf("remove %q: %w", path, err)
	}

	return nil
}

func (b *EmbeddedBackend) Bash(ctx context.Context, command, cwd string) (BashResult, error) {
	if command == "" {
		return BashResult{}, fmt.Errorf("command is empty")
	}
	
	if cwd == "" {
		cwd = b.RootDir
	} else {
		resolvedCwd, err := resolvePath(b.RootDir, cwd)
		if err != nil {
			return BashResult{}, err
		}
		cwd = resolvedCwd
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	result := BashResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return BashResult{}, fmt.Errorf("exec: %w", err)
	}
	return result, nil
}

// BashStream executes a command and calls onLine for each batch of output
// lines during execution. If onLine is nil, behaves identically to Bash.
// Throttles callbacks to at most once per 5 lines or 500ms, whichever
// comes first, to avoid flooding the bus with per-line events.
func (b *EmbeddedBackend) BashStream(ctx context.Context, command, cwd string, onLine func(line string)) (BashResult, error) {
	if onLine == nil {
		return b.Bash(ctx, command, cwd)
	}
	if command == "" {
		return BashResult{}, fmt.Errorf("command is empty")
	}
	if cwd == "" {
		cwd = b.RootDir
	} else {
		resolvedCwd, err := resolvePath(b.RootDir, cwd)
		if err != nil {
			return BashResult{}, err
		}
		cwd = resolvedCwd
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = cwd

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return BashResult{}, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return BashResult{}, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return BashResult{}, fmt.Errorf("start: %w", err)
	}

	type line struct{ text string }
	lineCh := make(chan line, 64)
	readers := 2
	var outBuf, errBuf strings.Builder

	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			lineCh <- line{text: scanner.Text()}
			outBuf.WriteString(scanner.Text())
			outBuf.WriteByte('\n')
		}
		_ = scanner.Err()
		lineCh <- line{} // signal done
	}()

	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			lineCh <- line{text: scanner.Text()}
			errBuf.WriteString(scanner.Text())
			errBuf.WriteByte('\n')
		}
		_ = scanner.Err()
		lineCh <- line{} // signal done
	}()

	var batch []string
	flush := func() {
		if len(batch) == 0 {
			return
		}
		onLine(strings.Join(batch, "\n"))
		batch = batch[:0]
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	const batchSize = 10
	for {
		select {
		case l := <-lineCh:
			if l.text == "" && readers > 0 {
				readers--
				if readers == 0 {
					flush()
					goto wait
				}
				continue
			}
			batch = append(batch, l.text)
			if len(batch) >= batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			flush()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return BashResult{}, ctx.Err()
		}
	}

wait:
	err = cmd.Wait()
	result := BashResult{
		Stdout: outBuf.String(),
		Stderr: errBuf.String(),
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return BashResult{}, fmt.Errorf("exec: %w", err)
	}
	return result, nil
}

// Grep runs `grep -rn` inside the workspace root and returns
// truncated results. Implements [GrepOperator].
func (b *EmbeddedBackend) Grep(ctx context.Context, pattern, path, include, exclude string) (string, error) {
	searchPath := path
	if searchPath == "" {
		searchPath = b.RootDir
	} else {
		resolved, err := resolvePath(b.RootDir, searchPath)
		if err != nil {
			return "", err
		}
		searchPath = resolved
	}

	cmdStr := fmt.Sprintf("grep -rn %s %s", ShellQuote(pattern), ShellQuote(searchPath))
	if include != "" {
		cmdStr += fmt.Sprintf(" --include=%s", ShellQuote(include))
	}
	if exclude != "" {
		cmdStr += fmt.Sprintf(" --exclude=%s", ShellQuote(exclude))
	}

	result, err := b.Bash(ctx, cmdStr, searchPath)
	if err != nil {
		return "", err
	}
	if result.ExitCode > 1 {
		return "", fmt.Errorf("grep failed (exit %d): %s", result.ExitCode, result.Stderr)
	}
	output := result.Stdout
	if output == "" {
		if result.ExitCode == 1 {
			return "no matches found", nil
		}
		return "(empty result)", nil
	}
	return TruncateOutput(output, maxGrepLines, maxGrepBytes), nil
}

// Find runs `find -name` inside the workspace root and returns
// truncated results. Implements [FindOperator].
func (b *EmbeddedBackend) Find(ctx context.Context, path, pattern string) (string, error) {
	searchPath := path
	if searchPath == "" {
		searchPath = b.RootDir
	} else {
		resolved, err := resolvePath(b.RootDir, searchPath)
		if err != nil {
			return "", err
		}
		searchPath = resolved
	}

	cmdStr := fmt.Sprintf("find %s -name %s", ShellQuote(searchPath), ShellQuote(pattern))
	result, err := b.Bash(ctx, cmdStr, searchPath)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 && result.Stderr != "" {
		return "", fmt.Errorf("find failed: %s", result.Stderr)
	}
	output := strings.TrimSpace(result.Stdout)
	if output == "" {
		return "no files found", nil
	}
	return TruncateOutput(output, maxGrepLines, maxGrepBytes), nil
}

