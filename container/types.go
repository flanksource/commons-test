package container

import (
	"context"
	"io"
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

// Config holds container configuration
type Config struct {
	Image        string
	Name         string
	Ports        map[string]string // container_port:host_port
	Env          []string
	Mounts       []Mount
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
