package helm

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/exec"
	"github.com/samber/lo"
)

// Pod represents a Kubernetes pod with fluent interface
type Pod struct {
	Metadata    `json:"metadata,omitempty"`
	Kind        `json:",inline"`
	selector    string
	container   string
	helm        *HelmChart
	colorOutput bool
	lastResult  *exec.ExecResult
	lastError   error
}

func (p *Pod) resolvePodName() error {
	if p.Name != "" {
		return nil
	}
	args := []any{"get", "pods", "-n", p.Namespace, "-l", p.selector,
		"-o", "jsonpath={.items[0].metadata.name}"}
	p.lastResult, p.lastError = kubectl(args...)
	p.Name = strings.TrimSpace(p.lastResult.Stdout)
	if p.Name == "" {
		return fmt.Errorf("no pod found with selector: %s", p.selector)
	}
	return nil
}

func (p *Pod) GetOwner() (*Object, error) {
	if len(p.OwnerReferences) == 0 {
		return nil, fmt.Errorf("no owner references found")
	}
	return lo.ToPtr(p.OwnerReferences[0].AsObject()), nil
}

// Container sets the container name
func (p *Pod) Container(name string) *Pod {
	p.container = name
	return p
}

// WaitReady waits for the pod to be ready
func (p *Pod) WaitReady() *Pod {
	return p.WaitFor("condition=Ready", 2*time.Minute)
}

// WaitFor waits for a specific condition
func (p *Pod) WaitFor(condition string, timeout time.Duration) *Pod {
	args := []any{"wait", "pod"}
	if p.Namespace != "" {
		args = append(args, "-n", p.Namespace)
	}
	if p.selector != "" {
		args = append(args, "-l", p.selector)
	}
	args = append(args, "--for="+condition, "--timeout="+timeout.String())

	p.lastResult, p.lastError = kubectl(args...)
	return p
}

// Exec executes a command in the pod
func (p *Pod) Exec(command string) *Pod {
	// Get pod name if not set
	if p.Name == "" && p.selector != "" {
		if err := p.resolvePodName(); err != nil {
			p.lastError = err
			return p
		}
	}

	args := []any{"exec", "-n", p.Namespace, p.Name}
	if p.container != "" {
		args = append(args, "-c", p.container)
	}
	args = append(args, "--", "bash", "-c", command)
	p.lastResult, p.lastError = kubectl(args...)
	return p
}

// GetLogs retrieves pod logs
func (p *Pod) GetLogs(lines ...int) string {
	// Get pod name if not set
	if p.Name == "" && p.selector != "" {
		if err := p.resolvePodName(); err != nil {
			p.lastError = err
			return ""
		}
	}

	args := []any{"logs", "-n", p.Namespace, p.Name}
	if p.container != "" {
		args = append(args, "-c", p.container)
	}
	if len(lines) > 0 {
		args = append(args, "--tail", fmt.Sprintf("%d", lines[0]))
	}

	p.lastResult, p.lastError = kubectl(args...)
	return p.lastResult.Stdout
}

func (p *Pod) GetName() string {
	p.resolvePodName()
	return p.Name
}

// Status returns the pod status
func (p *Pod) Status() (string, error) {
	// Get pod name if not set
	if p.Name == "" && p.selector != "" {
		if err := p.resolvePodName(); err != nil {
			return "", err
		}
	}

	args := []any{"get", "pod", p.GetName(), "-n", p.Namespace,
		"-o", "jsonpath={.status.phase}"}
	p.lastResult, p.lastError = kubectl(args...)
	return strings.TrimSpace(p.lastResult.Stdout), p.lastError
}

func getFreePort() int {
	// Use net.Listen to find a free port
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		panic(fmt.Sprintf("failed to get free port: %v", err))
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// ForwardPort forwards a port from the pod to the local machine
func (p *Pod) ForwardPort(port int) (*int, func()) {

	localPort := getFreePort()
	clicky.Infof("Forwarding pod %s port %d to local port %d", p.GetName(), port, localPort)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		resp, err := kubectl(exec.WithContext(ctx), "port-forward", "-n", p.Namespace, p.GetName(), fmt.Sprintf("%d:%d", localPort, port))
		if err != nil {
			clicky.Errorf("Port forward failed: %v", err)
			return
		} else {
			clicky.Infof("Port forward response: %v", resp)
		}
	}()

	start := time.Now()
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", localPort), 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Since(start) > 10*time.Second {
			clicky.Errorf("Timed out waiting for port forward to be ready")
			cancel()
			return nil, func() {}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return &localPort, func() {
		cancel()
	}
}

// Result returns the last command result
func (p *Pod) Result() string {
	return p.lastResult.Stdout
}

// Error returns the last error
func (p *Pod) Error() error {
	return p.lastError
}

// MustSucceed panics if there was an error
func (p *Pod) MustSucceed() *Pod {
	if p.lastError != nil {
		panic(p.lastError)
	}
	return p
}
