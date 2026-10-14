package agents

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"feishu-agent-gateway/internal/gateway"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Codex struct {
	binary, repo, home string
	runUID             uint32
}

func NewCodex(binary, repo, home string) (*Codex, error) {
	if binary == "" || repo == "" || home == "" || !filepath.IsAbs(home) {
		return nil, errors.New("Codex binary and repository are required")
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return nil, errors.New("Codex repository directory is invalid")
	}
	homeInfo, homeErr := os.Stat(home)
	if homeErr != nil || !homeInfo.IsDir() {
		return nil, errors.New("dedicated Codex home directory is invalid")
	}
	var uid uint64
	if value := os.Getenv("CODEX_RUN_UID"); value != "" {
		uid, err = strconv.ParseUint(value, 10, 32)
		if err != nil || uid == 0 {
			return nil, errors.New("CODEX_RUN_UID must be a non-root numeric UID")
		}
	}
	return &Codex{binary: binary, repo: abs, home: home, runUID: uint32(uid)}, nil
}

func (c *Codex) Models() []string  { return []string{"gpt-6-luna", "gpt-6-sol"} }
func (c *Codex) Efforts() []string { return []string{"low", "medium", "high", "xhigh"} }
func (c *Codex) Run(ctx context.Context, prompt, continuation, scope string) (string, string, error) {
	return c.RunWithOptions(ctx, prompt, continuation, scope, gateway.RunOptions{Reasoning: "high"})
}
func (c *Codex) RunWithOptions(ctx context.Context, prompt, continuation, _ string, opts gateway.RunOptions) (string, string, error) {
	if opts.Reasoning == "" {
		opts.Reasoning = "high"
	}
	if !allowedOption(c.Efforts(), opts.Reasoning) || opts.Model != "" && !allowedOption(c.Models(), opts.Model) {
		return "", "", errors.New("unsupported Codex model or reasoning effort")
	}

	if continuation != "" {
		if err := c.checkThread(ctx, continuation); err != nil {
			return "", "", err
		}
	}
	var args []string
	modelArgs := []string{"-c", `model_reasoning_effort="` + opts.Reasoning + `"`}
	if opts.Model != "" {
		modelArgs = append(modelArgs, "-c", `model="`+opts.Model+`"`)
	}
	if continuation == "" {
		args = append([]string{"exec", "--json", "--ignore-user-config", "--ignore-rules", "--sandbox", "read-only", "-C", c.repo}, modelArgs...)
		args = append(args, "--", prompt)
	} else {
		// `resume` has no --sandbox option; the config override pins it to read-only.
		args = append([]string{"exec", "resume", "--json", "-c", `sandbox_mode="read-only"`}, modelArgs...)
		args = append(args, continuation, "--", prompt)
	}
	cmd := exec.CommandContext(ctx, c.binary, args...)
	cmd.Dir = c.repo
	cmd.Env = c.childEnv()
	c.setChildIdentity(cmd)
	// The subprocess receives no chat-supplied flags or working directory.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", err
	}
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return "", "", fmt.Errorf("start Codex: %w", err)
	}
	reader := &io.LimitedReader{R: stdout, N: (4 << 20) + 1}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var threadID, answer string
	completed := false
	failed := false
	for scanner.Scan() {
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Item     struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			failed = true
			continue
		}
		switch event.Type {
		case "thread.started":
			threadID = event.ThreadID
		case "item.completed":
			if event.Item.Type == "agent_message" {
				answer = event.Item.Text
			}
		case "turn.completed":
			completed = true
		case "turn.failed":
			failed = true
		}
	}
	scanErr := scanner.Err()
	if reader.N <= 0 && scanErr == nil {
		scanErr = errors.New("Codex output exceeded 4 MiB limit")
	}
	// Scanner can stop before the child finishes writing. Kill before Wait,
	// otherwise a full stdout pipe can deadlock the child until the turn timeout.
	if scanErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return "", "", ctx.Err()
	}
	if scanErr != nil {
		return "", "", fmt.Errorf("read Codex output: %w", scanErr)
	}
	if waitErr != nil {
		return "", "", fmt.Errorf("Codex exited unsuccessfully: %w", waitErr)
	}
	if failed || !completed || threadID == "" || strings.TrimSpace(answer) == "" {
		return "", "", errors.New("Codex turn did not complete with a final answer")
	}
	if continuation != "" && threadID != continuation {
		return "", "", errors.New("Codex resumed unexpected thread")
	}
	return answer, threadID, nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 4096 {
		_, _ = b.Buffer.Write(p[:min(n, 4096-b.Len())])
	}
	return n, nil
}

// checkThread uses the documented read-only app-server thread/read method before
// sending a user prompt to `exec resume`: some CLI builds start a new thread on
// a missing ID rather than failing.
type codexRPCFrame struct {
	ID    int `json:"id"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	} `json:"result"`
}

func (c *Codex) checkThread(ctx context.Context, threadID string) error {
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, c.binary, "app-server", "--stdio")
	cmd.Dir = c.repo
	cmd.Env = c.childEnv()
	c.setChildIdentity(cmd)
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return err
	}
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		input.Close()
		return fmt.Errorf("start Codex thread check: %w", err)
	}
	defer func() { input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	writer := json.NewEncoder(input)
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	read := func(id int) (codexRPCFrame, error) {
		for scanner.Scan() {
			var frame codexRPCFrame
			if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame.ID != id {
				continue
			}
			return frame, nil
		}
		if checkCtx.Err() != nil {
			return codexRPCFrame{}, checkCtx.Err()
		}
		if scanner.Err() != nil {
			return codexRPCFrame{}, fmt.Errorf("read Codex thread check: %w", scanner.Err())
		}
		return codexRPCFrame{}, errors.New("Codex thread check did not return a result")
	}
	if err = writer.Encode(map[string]any{"method": "initialize", "id": 1, "params": map[string]any{"clientInfo": map[string]string{"name": "feishu-agent-gateway", "title": "Feishu Gateway", "version": "0.1"}}}); err != nil {
		return err
	}
	init, err := read(1)
	if err != nil {
		return err
	}
	if init.Error != nil {
		return fmt.Errorf("Codex initialize returned error %d", init.Error.Code)
	}
	if err = writer.Encode(map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return err
	}
	if err = writer.Encode(map[string]any{"method": "thread/read", "id": 2, "params": map[string]any{"threadId": threadID, "includeTurns": false}}); err != nil {
		return err
	}
	frame, err := read(2)
	if err != nil {
		return err
	}
	if frame.Error != nil {
		low := strings.ToLower(frame.Error.Message)
		if strings.Contains(low, "not loaded") || strings.Contains(low, "not found") {
			return gateway.ErrInvalidContinuation
		}
		return fmt.Errorf("Codex thread check returned error %d", frame.Error.Code)
	}
	if frame.Result.Thread.ID != threadID {
		return gateway.ErrInvalidContinuation
	}
	return nil
}

// childEnv is an allowlist, not inherited environment: the Codex process may
// execute model-requested read-only shell commands, which must never see the
// bot credential or other Agent API keys from the gateway process.
func (c *Codex) childEnv() []string {
	env := []string{"CODEX_HOME=" + c.home}
	if c.runUID != 0 {
		env = append(env, "HOME="+c.home)
	}
	for _, name := range []string{"HOME", "PATH", "TMPDIR", "LANG", "LC_ALL", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if name == "HOME" && c.runUID != 0 {
			continue
		}
		if value := os.Getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// setChildIdentity keeps the model-controlled Codex process in a less-privileged
// UID. The gateway can read its Feishu secret; the child must not.
func (c *Codex) setChildIdentity(cmd *exec.Cmd) {
	if c.runUID == 0 {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: c.runUID, Gid: c.runUID, NoSetGroups: true}}
}

func allowedOption(options []string, want string) bool {
	for _, value := range options {
		if value == want {
			return true
		}
	}
	return false
}
