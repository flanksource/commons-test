package helm

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/clicky/exec"
)

// Namespace represents a Kubernetes namespace with fluent interface
type Namespace struct {
	name        string
	colorOutput bool
	lastResult  *exec.ExecResult
	lastError   error
}

func (h *Namespace) GetPods(selectors ...string) ([]*Pod, error) {
	var pods []*Pod
	selector := strings.Join(selectors, ",")
	args := []any{"get", "pods", "-n", h.name,
		"-o", "json"}

	if selector != "" {
		args = append(args, "-l", selector)
	}
	result, err := kubectl(args...)
	if err != nil {
		return nil, err
	}

	var podList struct {
		Items []Pod `json:"items"`
	}

	if err := json.Unmarshal([]byte(result.Stdout), &podList); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pod list: %w", err)
	}

	for _, item := range podList.Items {
		pod := item
		pod.colorOutput = h.colorOutput
		pods = append(pods, &pod)
	}
	return pods, nil
}

// NewNamespace creates a new Namespace accessor
func NewNamespace(name string) *Namespace {
	return &Namespace{
		name:        name,
		colorOutput: true,
	}
}

// Create creates the namespace
func (n *Namespace) Create() *Namespace {
	n.lastResult, n.lastError = kubectl("create", "namespace", n.name)
	if n.lastError != nil && strings.Contains(n.lastResult.Stderr, "already exists") {
		// Namespace already exists, that's ok
		n.lastError = nil
	}
	return n
}

// Delete deletes the namespace
func (n *Namespace) Delete() *Namespace {
	n.lastResult, n.lastError = kubectl("delete", "namespace", n.name, "--wait=false")
	return n
}

// MustSucceed panics if there was an error
func (n *Namespace) MustSucceed() *Namespace {
	if n.lastError != nil {
		panic(n.lastError)
	}
	return n
}
