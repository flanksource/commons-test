package container

import (
	"context"
	"io"
	"time"
)

// Manager provides container management capabilities
type Manager interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Logs(ctx context.Context, follow bool) (io.ReadCloser, error)
	Exec(ctx context.Context, cmd []string) (string, error)
	IsRunning(ctx context.Context) (bool, error)
	GetPort(port string) (string, error)
	GetID() string
	Cleanup(ctx context.Context) error
}

// Mount represents a volume mount
type Mount struct {
	Source   string // Host path or volume name
	Target   string // Container path
	Type     string // "bind" or "volume"
	ReadOnly bool
}

// HealthCheck configures a Docker health check on the container.
type HealthCheck struct {
	Cmd         string        // Command to run (e.g. "curl -f http://localhost:3100/ready")
	Interval    time.Duration // Time between checks (default 10s)
	Timeout     time.Duration // Max time for a single check (default 5s)
	Retries     int           // Consecutive failures before unhealthy (default 3)
	StartPeriod time.Duration // Grace period before checks count (default 0s)
}

// Config holds container configuration
type Config struct {
	Image        string
	Name         string
	Ports        map[string]string // container_port:host_port
	Env          []string
	Mounts       []Mount
	HealthCheck  *HealthCheck
	WaitStrategy WaitStrategy
	Reuse        bool
}

// WaitStrategy defines how to wait for container readiness
type WaitStrategy struct {
	Port     string
	LogMatch string
	Timeout  string
}

// ContainerInfo holds information about an existing container
type ContainerInfo struct {
	ID     string
	Name   string
	Status string
	Ports  map[string]string
}

// ExecResult holds the result of a container execution
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}
