package container

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	. "github.com/onsi/ginkgo/v2"
)

const (
	DefaultTimeout      = 5 * time.Minute
)

// Container manages Docker containers with transparent reuse
type Container struct {
	config      Config
	client      *client.Client
	containerID string
	isRunning   bool
}

// New creates a new Container manager
func New(config Config) (*Container, error) {
	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	return &Container{
		config: config,
		client: cli,
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

	timeout := int(30) // 30 seconds
	if err := c.client.ContainerStop(ctx, c.containerID, container.StopOptions{
		Timeout: &timeout,
	}); err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}

	c.isRunning = false
	return nil
}

// Logs returns container logs
func (c *Container) Logs(ctx context.Context, follow bool) (io.ReadCloser, error) {
	if c.containerID == "" {
		return nil, fmt.Errorf("container not started")
	}

	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Timestamps: true,
	}

	logs, err := c.client.ContainerLogs(ctx, c.containerID, options)
	if err != nil {
		return nil, fmt.Errorf("failed to get container logs: %w", err)
	}

	return logs, nil
}

// Exec executes a command in the container
func (c *Container) Exec(ctx context.Context, cmd []string) (string, error) {
	if c.containerID == "" {
		return "", fmt.Errorf("container not started")
	}

	// Create exec instance
	execConfig := container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
	}

	execID, err := c.client.ContainerExecCreate(ctx, c.containerID, execConfig)
	if err != nil {
		return "", fmt.Errorf("failed to create exec instance: %w", err)
	}

	// Start exec
	resp, err := c.client.ContainerExecAttach(ctx, execID.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to attach to exec instance: %w", err)
	}
	defer resp.Close()

	// Read output
	output, err := io.ReadAll(resp.Reader)
	if err != nil {
		return "", fmt.Errorf("failed to read exec output: %w", err)
	}

	// Check exec result
	inspect, err := c.client.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return "", fmt.Errorf("failed to inspect exec result: %w", err)
	}

	if inspect.ExitCode != 0 {
		return string(output), fmt.Errorf("command failed with exit code %d: %s", inspect.ExitCode, string(output))
	}

	return string(output), nil
}

// IsRunning checks if the container is running
func (c *Container) IsRunning(ctx context.Context) (bool, error) {
	if c.containerID == "" {
		return false, nil
	}

	inspect, err := c.client.ContainerInspect(ctx, c.containerID)
	if err != nil {
		return false, fmt.Errorf("failed to inspect container: %w", err)
	}

	c.isRunning = inspect.State.Running
	return c.isRunning, nil
}

// GetPort returns the host port for a container port
func (c *Container) GetPort(port string) (string, error) {
	if c.containerID == "" {
		return "", fmt.Errorf("container not started")
	}

	inspect, err := c.client.ContainerInspect(context.Background(), c.containerID)
	if err != nil {
		return "", fmt.Errorf("failed to inspect container: %w", err)
	}

	portKey := port + "/tcp"
	if portBindings, exists := inspect.NetworkSettings.Ports[nat.Port(portKey)]; exists && len(portBindings) > 0 {
		return portBindings[0].HostPort, nil
	}

	return "", fmt.Errorf("no port mapping found for port %s", port)
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
	if err := c.client.ContainerRemove(ctx, c.containerID, container.RemoveOptions{
		Force: true,
	}); err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}

	c.containerID = ""
	return nil
}

// Close closes the Docker client
func (c *Container) Close() error {
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

// findAndReuseContainer tries to find and reuse an existing container
func (c *Container) findAndReuseContainer(ctx context.Context) error {
	containers, err := c.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	// Look for container by name
	for _, ctr := range containers {
		for _, name := range ctr.Names {
			cleanName := strings.TrimPrefix(name, "/")
			if cleanName == c.config.Name {
				c.containerID = ctr.ID
				c.isRunning = strings.Contains(ctr.Status, "Up")
				return nil
			}
		}
	}

	return fmt.Errorf("container with name %s not found", c.config.Name)
}

// ensureContainerRunning starts the container if it's not running
func (c *Container) ensureContainerRunning(ctx context.Context) error {
	if c.isRunning {
		return nil
	}

	if err := c.client.ContainerStart(ctx, c.containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start existing container: %w", err)
	}

	c.isRunning = true
	return nil
}

// createAndStartContainer creates and starts a new container
func (c *Container) createAndStartContainer(ctx context.Context) error {
	// Pull image if needed
	if _, _, err := c.client.ImageInspectWithRaw(ctx, c.config.Image); err != nil {
		c.Infof("Pulling image %s...", c.config.Image)
		_, err := c.client.ImagePull(ctx, c.config.Image, image.PullOptions{})
		if err != nil {
			c.Errorf("Failed to pull image: %v", err)
			return fmt.Errorf("failed to pull image: %w", err)
		}
		c.Infof("Successfully pulled image %s", c.config.Image)
	}

	// Prepare port bindings
	portBindings := nat.PortMap{}
	exposedPorts := nat.PortSet{}
	for containerPort, hostPort := range c.config.Ports {
		port := nat.Port(containerPort + "/tcp")
		exposedPorts[port] = struct{}{}
		portBindings[port] = []nat.PortBinding{
			{
				HostPort: hostPort,
			},
		}
	}

	// Create container
	containerConfig := &container.Config{
		Image:        c.config.Image,
		Env:          c.config.Env,
		ExposedPorts: exposedPorts,
	}

	// Prepare mounts
	var mounts []mount.Mount
	for _, m := range c.config.Mounts {
		mountType := mount.TypeBind
		if m.Type == "volume" {
			mountType = mount.TypeVolume
		}
		mounts = append(mounts, mount.Mount{
			Type:     mountType,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}

	hostConfig := &container.HostConfig{
		PortBindings: portBindings,
		Mounts:       mounts,
		AutoRemove:   false, // Never auto-remove to prevent immediate deletion on exit
	}

	networkConfig := &network.NetworkingConfig{}

	var containerName string
	if c.config.Name != "" {
		containerName = c.config.Name
	}

	resp, err := c.client.ContainerCreate(ctx, containerConfig, hostConfig, networkConfig, nil, containerName)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	c.containerID = resp.ID

	// Start container
	c.Infof("Starting container...")
	if err := c.client.ContainerStart(ctx, c.containerID, container.StartOptions{}); err != nil {
		c.PrintLogsOnFailure(ctx, fmt.Sprintf("Failed to start container: %v", err))
		return fmt.Errorf("failed to start container: %w", err)
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
	if inspect, err := c.client.ContainerInspect(ctx, c.containerID); err == nil {
		c.Errorf("Container Status - State: %s, ExitCode: %d, Error: %s",
			inspect.State.Status, inspect.State.ExitCode, inspect.State.Error)
		c.Errorf("Container Started At: %s, Finished At: %s",
			inspect.State.StartedAt, inspect.State.FinishedAt)
	} else {
		c.Errorf("Failed to inspect container for status: %v", err)
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

		// Check container state
		inspect, err := c.client.ContainerInspect(ctx, c.containerID)
		if err != nil {
			c.Errorf("Failed to inspect container during stability check: %v", err)
			c.PrintLogsOnFailure(ctx, fmt.Sprintf("Failed to inspect container: %v", err))
			return fmt.Errorf("failed to inspect container: %w", err)
		}

		if !inspect.State.Running {
			failureReason := fmt.Sprintf("Container exited unexpectedly - Status: %s, ExitCode: %d",
				inspect.State.Status, inspect.State.ExitCode)
			c.PrintLogsOnFailure(ctx, failureReason)
			return fmt.Errorf("container exited with status %s (exit code: %d)",
				inspect.State.Status, inspect.State.ExitCode)
		}

		// Container is running, wait before next check
		time.Sleep(checkInterval)
	}

	c.Infof("Container stability check passed - running consistently for %v", stabilityPeriod)
	return nil
}
