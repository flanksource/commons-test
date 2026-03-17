package container

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/flanksource/clicky"
	"github.com/flanksource/commons/logger"
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
		Logger: logger.GetLogger("docker").Named(config.Name),
		config: config,
	}, nil
}

// Start starts or reuses an existing container
func (c *Container) Start(ctx context.Context) error {
	// Try to find and reuse existing container if enabled
	if c.config.Reuse {
		if err := c.findAndReuseContainer(ctx); err != nil {
			// Log warning but continue to create new container
			c.Warnf("Failed to reuse existing container: %v", err)
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
		return nil // not found — caller will create a new container
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

// ensureContainerRunning starts the container if it's not running and waits for readiness.
func (c *Container) ensureContainerRunning(ctx context.Context) error {
	if c.isRunning {
		return c.waitForStableState(ctx)
	}

	process := clicky.Exec("docker", "start", c.containerID).Run()
	if process.Err != nil {
		return fmt.Errorf("failed to start existing container: %w", process.Err)
	}

	c.isRunning = true
	return c.waitForStableState(ctx)
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

	// Add health check
	if hc := c.config.HealthCheck; hc != nil {
		args = append(args, "--health-cmd", hc.Cmd)
		if hc.Interval > 0 {
			args = append(args, "--health-interval", hc.Interval.String())
		}
		if hc.Timeout > 0 {
			args = append(args, "--health-timeout", hc.Timeout.String())
		}
		if hc.Retries > 0 {
			args = append(args, "--health-retries", fmt.Sprintf("%d", hc.Retries))
		}
		if hc.StartPeriod > 0 {
			args = append(args, "--health-start-period", hc.StartPeriod.String())
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

// waitForStableState waits for the container to reach a stable running state.
// If a health check is configured, it waits for the container to become healthy.
// Otherwise, it waits for all exposed ports to accept TCP connections.
func (c *Container) waitForStableState(ctx context.Context) error {
	if c.config.HealthCheck != nil {
		return c.waitForHealthy(ctx)
	}
	return c.waitForPorts(ctx)
}

// waitForPorts polls all exposed ports until they accept TCP connections.
func (c *Container) waitForPorts(ctx context.Context) error {
	if len(c.config.Ports) == 0 {
		return nil
	}

	timeout := 2 * time.Minute
	deadline := time.Now().Add(timeout)

	for containerPort := range c.config.Ports {
		hostPort, err := c.GetPort(containerPort)
		if err != nil {
			return fmt.Errorf("get host port for %s: %w", containerPort, err)
		}

		addr := "localhost:" + hostPort
		c.Infof("Waiting for port %s (host %s)...", containerPort, hostPort)

		ready := false
		checks := 0
		for time.Now().Before(deadline) {
			conn, dialErr := net.DialTimeout("tcp", addr, 2*time.Second)
			if dialErr == nil {
				conn.Close()
				c.Infof("Port %s is ready", containerPort)
				ready = true
				break
			}
			c.Tracef("Port %s dial failed: %v", containerPort, dialErr)

			checks++
			if checks%10 == 0 {
				running, _ := c.IsRunning(ctx)
				if !running {
					diag := c.containerDiagnostics()
					c.PrintLogsOnFailure(ctx, fmt.Sprintf("Container stopped while waiting for port %s: %s", containerPort, diag))
					return fmt.Errorf("container stopped while waiting for port %s: %s", containerPort, diag)
				}
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			time.Sleep(500 * time.Millisecond)
		}

		if !ready {
			diag := c.containerDiagnostics()
			c.PrintLogsOnFailure(ctx, fmt.Sprintf("Port %s not ready after %v: %s", containerPort, timeout, diag))
			return fmt.Errorf("timed out waiting for port %s: %s", containerPort, diag)
		}
	}
	return nil
}

// containerDiagnostics returns a human-readable summary of why the container
// is not ready: state, health check output, and port reachability.
func (c *Container) containerDiagnostics() string {
	var diag []string

	// Container state
	proc := clicky.Exec("docker", "inspect", "--format", "{{json .State}}", c.containerID).Run()
	if proc.Err == nil {
		var state struct {
			Status   string `json:"Status"`
			Running  bool   `json:"Running"`
			ExitCode int    `json:"ExitCode"`
			Error    string `json:"Error"`
		}
		if err := json.Unmarshal([]byte(proc.GetStdout()), &state); err == nil {
			if !state.Running {
				diag = append(diag, fmt.Sprintf("container state=%s (not running), exitCode=%d", state.Status, state.ExitCode))
				if state.Error != "" {
					diag = append(diag, fmt.Sprintf("docker error: %s", state.Error))
				}
			} else {
				diag = append(diag, fmt.Sprintf("container state=%s", state.Status))
			}
		}
	}

	// Health check output (if configured)
	if c.config.HealthCheck != nil {
		hProc := clicky.Exec("docker", "inspect", "--format", "{{json .State.Health}}", c.containerID).Run()
		if hProc.Err == nil {
			var health struct {
				Status string `json:"Status"`
				Log    []struct {
					Output   string `json:"Output"`
					ExitCode int    `json:"ExitCode"`
				} `json:"Log"`
			}
			if err := json.Unmarshal([]byte(hProc.GetStdout()), &health); err == nil {
				diag = append(diag, fmt.Sprintf("healthcheck status=%s, cmd=%q", health.Status, c.config.HealthCheck.Cmd))
				if len(health.Log) > 0 {
					last := health.Log[len(health.Log)-1]
					diag = append(diag, fmt.Sprintf("last healthcheck: exit=%d output=%s", last.ExitCode, strings.TrimSpace(last.Output)))
				}
			}
		}
	}

	// Port reachability
	for containerPort := range c.config.Ports {
		hostPort, err := c.GetPort(containerPort)
		if err != nil {
			diag = append(diag, fmt.Sprintf("port %s: unable to resolve host port: %v", containerPort, err))
			continue
		}
		conn, err := net.DialTimeout("tcp", "localhost:"+hostPort, 1*time.Second)
		if err != nil {
			diag = append(diag, fmt.Sprintf("port %s (host %s): not listening", containerPort, hostPort))
		} else {
			conn.Close()
			diag = append(diag, fmt.Sprintf("port %s (host %s): listening", containerPort, hostPort))
		}
	}

	if len(diag) == 0 {
		return "no diagnostics available"
	}
	return strings.Join(diag, "; ")
}

func (c *Container) waitForHealthy(ctx context.Context) error {
	timeout := 2 * time.Minute
	checkInterval := 2 * time.Second
	deadline := time.Now().Add(timeout)

	c.Infof("Waiting up to %v for container to become healthy...", timeout)

	firstCheck := true
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		process := clicky.Exec("docker", "inspect", "--format", "{{.State.Health.Status}}", c.containerID).Run()
		if process.Err == nil {
			status := strings.TrimSpace(process.GetStdout())
			c.Tracef("Health status: %s", status)

			if firstCheck && (status == "" || status == "<no value>") {
				c.Warnf("No Docker health check registered on container, falling back to port readiness")
				return c.waitForPorts(ctx)
			}

			switch status {
			case "healthy":
				c.Infof("Container is healthy")
				return nil
			case "unhealthy":
				diag := c.containerDiagnostics()
				c.PrintLogsOnFailure(ctx, fmt.Sprintf("Container became unhealthy: %s", diag))
				return fmt.Errorf("container became unhealthy: %s", diag)
			}
		} else if firstCheck {
			c.Warnf("Health check inspect failed, falling back to port readiness: %v", process.Err)
			return c.waitForPorts(ctx)
		} else {
			c.Tracef("Health check inspect failed: %v", process.Err)
		}

		firstCheck = false
		time.Sleep(checkInterval)
	}

	diag := c.containerDiagnostics()
	c.PrintLogsOnFailure(ctx, fmt.Sprintf("Timed out waiting for healthy status: %s", diag))
	return fmt.Errorf("timed out waiting for container to become healthy: %s", diag)
}
