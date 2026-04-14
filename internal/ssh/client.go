package ssh

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Client handles SSH command execution by wrapping the system's ssh binary.
type Client struct {
	host    string
	port    int
	user    string
	keyPath string
}

// Result contains the output and metadata of an executed command.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Elapsed  time.Duration
}

// NewClient creates a new SSH client with the specified connection details.
func NewClient(host string, port int, user, keyPath string) *Client {
	return &Client{
		host:    host,
		port:    port,
		user:    user,
		keyPath: expandHome(keyPath),
	}
}


// Run executes a command on the remote server and returns the result.
func (c *Client) Run(command string) (*Result, error) {
	start := time.Now()
	cmd := exec.Command("ssh", c.buildArgs(command)...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := &Result{
		Stdout:  strings.TrimSpace(stdout.String()),
		Stderr:  strings.TrimSpace(stderr.String()),
		Elapsed: time.Since(start),
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, fmt.Errorf("ssh exec: %w", err)
	}

	return result, nil
}

// RunOrFail executes a command and returns an error if the connection fails or the exit code is non-zero.
func (c *Client) RunOrFail(command string) (*Result, error) {
	res, err := c.Run(command)
	if err != nil {
		return res, err
	}
	if res.ExitCode != 0 {
		errMsg := res.Stderr
		if errMsg == "" {
			errMsg = res.Stdout
		}
		return res, fmt.Errorf("exit %d: %s", res.ExitCode, errMsg)
	}
	return res, nil
}

// RunSudo executes a command with sudo on the remote server.
func (c *Client) RunSudo(command string) (*Result, error) {
	return c.RunOrFail("sudo " + command)
}


func (c *Client) buildArgs(command string) []string {
	return []string{
		"-i", c.keyPath,
		"-p", fmt.Sprintf("%d", c.port),
		"-o", "StrictHostKeyChecking=no",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=30",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		fmt.Sprintf("%s@%s", c.user, c.host),
		command,
	}
}

// Test verifies the SSH connection and checks for the presence of required tools on the remote server.
func (c *Client) Test() error {
	res, err := c.RunOrFail("echo ok")
	if err != nil {
		return fmt.Errorf("SSH failed: %w", err)
	}
	if res.Stdout != "ok" {
		return fmt.Errorf("unexpected response: %q", res.Stdout)
	}

	tools := []struct{ cmd, name string }{
		{"which wp", "wp-cli"},
		{"which rsync", "rsync"},
		{"which mysql", "mysql"},
		{"which mysqldump", "mysqldump"},
		{"which nginx", "nginx"},
		{"which certbot", "certbot"},
	}

	fmt.Println("\n  Checking tools on the server:")
	allOk := true
	for _, t := range tools {
		r, _ := c.Run(t.cmd)
		if r != nil && r.ExitCode == 0 {
			fmt.Printf("  ✅ %-12s %s\n", t.name, r.Stdout)
		} else {
			fmt.Printf("  ❌ %-12s not found\n", t.name)
			allOk = false
		}
	}

	if !allOk {
		return fmt.Errorf("not all tools are available")
	}
	return nil
}

// Close releases any resources used by the client (currently a no-op as the binary is used).
func (c *Client) Close() {}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return strings.Replace(path, "~", home, 1)
		}
	}
	return path
}