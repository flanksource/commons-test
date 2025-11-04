package container

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/flanksource/clicky"
	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
)

const (
	DefaultTimeout = 5 * time.Minute
)

// Container manages Docker containers with transparent reuse
type Container struct {
	logger.Logger
	config      Config
	containerID string
	isRunning   bool
}

// New creates a new Container manager
func New(config Config) (*Container, error) {
	return &Container{
		Logger: logger.GetLogger(),
		config: config,
	}, nil
}

// Start starts or reuses an existing container
func (c *Container) Start(ctx context.Context) error {
	// Try to find and reuse existing container if enabled
	if c.config.Reuse {
		if err := c.findAndReuseContainer(ctx); err != nil {
			// Log warning but continue to create new container
			fmt.Printf("Warning: failed to reuse existing container: %v\n", err)
		} else if c.containerID != "" {
			// Successfully reused container
			return c.ensureContainerRunning(ctx)
		}
	}

	// Create new container
	return c.createAndStartContainer(ctx)
}

// Stop stops the container
func (c *Container) Stop(ctx context.Context) error {
	if c.containerID == "" {
		return nil
	}

	process := clicky.Exec("docker", "stop", "-t", "30", c.containerID).Run()
	if process.Err != nil {
		return fmt.Errorf("failed to stop container: %w", process.Err)
	}

	c.isRunning = false
	return nil
}

// Logs returns container logs
func (c *Container) Logs(ctx context.Context, follow bool) (io.ReadCloser, error) {
	if c.containerID == "" {
		return nil, fmt.Errorf("container not started")
	}

	args := []string{"docker", "logs", "--timestamps"}
	if follow {
		args = append(args, "--follow")
	}
	args = append(args, c.containerID)

	process := clicky.Exec(args[0], args[1:]...).Run()
	if process.Err != nil {
		return nil, fmt.Errorf("failed to get container logs: %w", process.Err)
	}

	return io.NopCloser(bytes.NewBufferString(process.Out())), nil
}

// Exec executes a command in the container
func (c *Container) Exec(ctx context.Context, cmd []string) (string, error) {
	if c.containerID == "" {
		return "", fmt.Errorf("container not started")
	}

	// Build docker exec command
	args := []string{"docker", "exec", c.containerID}
	args = append(args, cmd...)

	process := clicky.Exec(args[0], args[1:]...).Run()

	if process.Err != nil {
		return "", fmt.Errorf("command failed: %w", process.Err)
	}

	return process.Out(), nil
}

// IsRunning checks if the container is running
func (c *Container) IsRunning(ctx context.Context) (bool, error) {
	if c.containerID == "" {
		return false, nil
	}

	process := clicky.Exec("docker", "inspect", "--format", "{{.State.Running}}", c.containerID).Run()
	if process.Err != nil {
		return false, fmt.Errorf("failed to inspect container: %w", process.Err)
	}

	running := strings.TrimSpace(process.GetStdout()) == "true"
	c.isRunning = running
	return c.isRunning, nil
}

// GetPort returns the host port for a container port
func (c *Container) GetPort(port string) (string, error) {
	if c.containerID == "" {
		return "", fmt.Errorf("container not started")
	}

	// Use docker inspect with format to get port mapping
	format := fmt.Sprintf("{{(index (index .NetworkSettings.Ports \"%s/tcp\") 0).HostPort}}", port)
	process := clicky.Exec("docker", "inspect", "--format", format, c.containerID).Run()
	if process.Err != nil {
		return "", fmt.Errorf("failed to inspect container: %w", process.Err)
	}

	hostPort := strings.TrimSpace(process.GetStdout())
	if hostPort == "" || hostPort == "<no value>" {
		return "", fmt.Errorf("no port mapping found for port %s", port)
	}

	return hostPort, nil
}

// GetID returns the container ID
func (c *Container) GetID() string {
	return c.containerID
}

// Cleanup removes the container
func (c *Container) Cleanup(ctx context.Context) error {
	if c.containerID == "" {
		return nil
	}

	// Don't remove if reuse is enabled - just stop it
	if c.config.Reuse {
		return c.Stop(ctx)
	}

	// Stop container first
	if err := c.Stop(ctx); err != nil {
		c.Errorf("Failed to stop container before cleanup: %v", err)
	}

	// Remove container
	process := clicky.Exec("docker", "rm", "-f", c.containerID).Run()
	if process.Err != nil {
		return fmt.Errorf("failed to remove container: %w", process.Err)
	}

	c.containerID = ""
	return nil
}

// findAndReuseContainer tries to find and reuse an existing container
func (c *Container) findAndReuseContainer(ctx context.Context) error {
	// Use docker ps to list containers with matching name
	process := clicky.Exec("docker", "ps", "-a", "--filter", fmt.Sprintf("name=^%s$", c.config.Name), "--format", "{{.ID}}\t{{.Status}}").Run()
	if process.Err != nil {
		return fmt.Errorf("failed to list containers: %w", process.Err)
	}

	output := strings.TrimSpace(process.GetStdout())
	if output == "" {
		return fmt.Errorf("container with name %s not found", c.config.Name)
	}

	// Parse output: ID<tab>Status
	parts := strings.Split(output, "\t")
	if len(parts) < 2 {
		return fmt.Errorf("unexpected output format from docker ps")
	}

	c.containerID = parts[0]
	c.isRunning = strings.HasPrefix(parts[1], "Up")
	return nil
}

// ensureContainerRunning starts the container if it's not running
func (c *Container) ensureContainerRunning(ctx context.Context) error {
	if c.isRunning {
		return nil
	}

	process := clicky.Exec("docker", "start", c.containerID).Run()
	if process.Err != nil {
		return fmt.Errorf("failed to start existing container: %w", process.Err)
	}

	c.isRunning = true
	return nil
}

// createAndStartContainer creates and starts a new container
func (c *Container) createAndStartContainer(ctx context.Context) error {
	// Check if image exists, pull if needed
	checkProcess := clicky.Exec("docker", "image", "inspect", c.config.Image).Run()
	if checkProcess.Err != nil {
		c.Infof("Pulling image %s...", c.config.Image)
		pullProcess := clicky.Exec("docker", "pull", c.config.Image).Run()
		if pullProcess.Err != nil {
			c.Errorf("Failed to pull image: %v", pullProcess.Err)
			return fmt.Errorf("failed to pull image: %w", pullProcess.Err)
		}
		c.Infof("Successfully pulled image %s", c.config.Image)
	}

	// Build docker create command
	args := []string{"docker", "create"}

	// Add name if specified
	if c.config.Name != "" {
		args = append(args, "--name", c.config.Name)
	}

	// Add port bindings
	for containerPort, hostPort := range c.config.Ports {
		args = append(args, "-p", fmt.Sprintf("%s:%s", hostPort, containerPort))
	}

	// Add environment variables
	for _, env := range c.config.Env {
		args = append(args, "-e", env)
	}

	// Add mounts
	for _, m := range c.config.Mounts {
		mountStr := fmt.Sprintf("%s:%s", m.Source, m.Target)
		if m.ReadOnly {
			mountStr += ":ro"
		}
		if m.Type == "volume" {
			args = append(args, "-v", mountStr)
		} else {
			args = append(args, "--mount", fmt.Sprintf("type=bind,source=%s,target=%s", m.Source, m.Target))
		}
	}

	// Add image
	args = append(args, c.config.Image)

	// Create container
	createProcess := clicky.Exec(args[0], args[1:]...).Run()
	if createProcess.Err != nil {
		return fmt.Errorf("failed to create container: %w", createProcess.Err)
	}

	c.containerID = strings.TrimSpace(createProcess.GetStdout())

	// Start container
	c.Infof("Starting container...")
	startProcess := clicky.Exec("docker", "start", c.containerID).Run()
	if startProcess.Err != nil {
		c.PrintLogsOnFailure(ctx, fmt.Sprintf("Failed to start container: %v", startProcess.Err))
		return fmt.Errorf("failed to start container: %w", startProcess.Err)
	}

	c.Infof("Container started, verifying it remains running...")

	// Wait for container to stabilize and verify it stays running
	if err := c.waitForStableState(ctx); err != nil {
		c.Errorf("Container failed to maintain stable state: %v", err)
		return fmt.Errorf("container failed to maintain stable state: %w", err)
	}

	c.isRunning = true
	c.Infof("Container startup completed successfully")
	return nil
}

// Infof logs an informational message with container name prefix
func (c *Container) Infof(format string, args ...interface{}) {
	GinkgoWriter.Printf("[%s] %s\n", c.config.Name, fmt.Sprintf(format, args...))
}

// Errorf logs an error message with container name prefix
func (c *Container) Errorf(format string, args ...interface{}) {
	GinkgoWriter.Printf("[%s] ERROR: %s\n", c.config.Name, fmt.Sprintf(format, args...))
}

// PrintLogsOnFailure prints container logs when startup fails
func (c *Container) PrintLogsOnFailure(ctx context.Context, reason string) {
	c.Errorf("Container startup failed: %s", reason)

	if c.containerID == "" {
		c.Errorf("No container ID available for log retrieval")
		return
	}

	// Try to get container status first
	process := clicky.Exec("docker", "inspect", "--format", "{{json .State}}", c.containerID).Run()
	if process.Err == nil {
		var state struct {
			Status     string `json:"Status"`
			ExitCode   int    `json:"ExitCode"`
			Error      string `json:"Error"`
			StartedAt  string `json:"StartedAt"`
			FinishedAt string `json:"FinishedAt"`
		}
		if err := json.Unmarshal([]byte(process.GetStdout()), &state); err == nil {
			c.Errorf("Container Status - State: %s, ExitCode: %d, Error: %s",
				state.Status, state.ExitCode, state.Error)
			c.Errorf("Container Started At: %s, Finished At: %s",
				state.StartedAt, state.FinishedAt)
		}
	} else {
		c.Errorf("Failed to inspect container for status: %v", process.Err)
	}

	// Get container logs
	c.Infof("Retrieving container logs for debugging...")
	logs, err := c.Logs(ctx, false)
	if err != nil {
		c.Errorf("Failed to retrieve container logs: %v", err)
		return
	}
	defer logs.Close()

	// Read and display logs (limit to reasonable size)
	buffer := make([]byte, 8192) // 8KB of logs
	n, err := logs.Read(buffer)
	if err != nil && err != io.EOF {
		c.Errorf("Failed to read container logs: %v", err)
		return
	}

	if n > 0 {
		c.Errorf("=== Container Logs (last %d bytes) ===", n)
		c.Errorf("%s", string(buffer[:n]))
		c.Errorf("=== End Container Logs ===")
	} else {
		c.Errorf("No container logs available")
	}
}

// waitForStableState waits for the container to reach a stable running state
func (c *Container) waitForStableState(ctx context.Context) error {
	// Wait a bit for container to fully start up
	stabilityPeriod := 3 * time.Second
	checkInterval := 500 * time.Millisecond
	maxChecks := int(stabilityPeriod / checkInterval)

	c.Infof("Monitoring container stability for %v...", stabilityPeriod)

	for i := 0; i < maxChecks; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Check container state using docker inspect
		process := clicky.Exec("docker", "inspect", "--format", "{{json .State}}", c.containerID).Run()
		if process.Err != nil {
			c.Errorf("Failed to inspect container during stability check: %v", process.Err)
			c.PrintLogsOnFailure(ctx, fmt.Sprintf("Failed to inspect container: %v", process.Err))
			return fmt.Errorf("failed to inspect container: %w", process.Err)
		}

		// Parse the state JSON
		var state struct {
			Running  bool   `json:"Running"`
			Status   string `json:"Status"`
			ExitCode int    `json:"ExitCode"`
		}
		if err := json.Unmarshal([]byte(process.GetStdout()), &state); err != nil {
			return fmt.Errorf("failed to parse container state: %w", err)
		}

		if !state.Running {
			failureReason := fmt.Sprintf("Container exited unexpectedly - Status: %s, ExitCode: %d",
				state.Status, state.ExitCode)
			c.PrintLogsOnFailure(ctx, failureReason)
			return fmt.Errorf("container exited with status %s (exit code: %d)",
				state.Status, state.ExitCode)
		}

		// Container is running, wait before next check
		time.Sleep(checkInterval)
	}

	c.Infof("Container stability check passed - running consistently for %v", stabilityPeriod)
	return nil
}
