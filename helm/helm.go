package helm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/clicky"
	clickyExec "github.com/flanksource/clicky/exec"
	flanksourceCtx "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-test/command"
	"github.com/flanksource/gomplate/v3/base64"
	"sigs.k8s.io/yaml"
)

type Helm = clickyExec.WrapperFunc

var kubectl clickyExec.WrapperFunc = clicky.Exec("kubectl").AsWrapper()
var helm clickyExec.WrapperFunc = clicky.Exec("helm").AsWrapper()

// HelmChart represents a Helm chart with fluent interface
type HelmChart struct {
	flanksourceCtx.Context
	releaseName string
	namespace   string
	chartPath   string
	values      map[string]interface{}
	wait        bool
	timeout     time.Duration
	colorOutput bool
	dryRun      bool
	helm        Helm

	lastResult *clickyExec.ExecResult
	lastError  error
}

// NewHelmChart creates a new HelmChart builder
func NewHelmChart(ctx flanksourceCtx.Context, chartPath string) *HelmChart {
	return &HelmChart{
		Context:     ctx,
		chartPath:   chartPath,
		colorOutput: true,
		timeout:     5 * time.Minute,
		values:      make(map[string]interface{}),
	}
}

// Release sets the release name
func (h *HelmChart) Release(name string) *HelmChart {
	h.releaseName = name
	return h
}

// Namespace sets the namespace
func (h *HelmChart) Namespace(ns string) *HelmChart {
	h.namespace = ns
	return h
}

// Values sets or merges Helm values
func (h *HelmChart) Values(values map[string]interface{}) *HelmChart {
	for k, v := range values {
		h.values[k] = v
	}
	return h
}

// SetValue sets a single value using dot notation
func (h *HelmChart) SetValue(key string, value interface{}) *HelmChart {
	parts := strings.Split(key, ".")
	m := h.values
	for i, part := range parts {
		if i == len(parts)-1 {
			m[part] = value
		} else {
			if _, ok := m[part]; !ok {
				m[part] = make(map[string]interface{})
			}
			m = m[part].(map[string]interface{})
		}
	}
	return h
}

func (h *HelmChart) GetValues() map[string]interface{} {
	return h.values
}

func (h *HelmChart) GetValue(path ...string) string {
	return h.Context.Lookup(h.namespace).WithHelmRef(h.releaseName, strings.Join(path, ".")).MustGetString()
}

// Wait enables waiting for resources to be ready
func (h *HelmChart) Wait() *HelmChart {
	h.wait = true
	return h
}

// WaitFor sets the wait timeout
func (h *HelmChart) WaitFor(timeout time.Duration) *HelmChart {
	h.wait = true
	h.timeout = timeout
	return h
}

// DryRun enables dry-run mode
func (h *HelmChart) DryRun() *HelmChart {
	h.dryRun = true
	return h
}

// NoColor disables colored output
func (h *HelmChart) NoColor() *HelmChart {
	h.colorOutput = false
	return h
}

// Install installs the Helm chart
func (h *HelmChart) Install() *HelmChart {
	if h.releaseName == "" {
		h.lastError = fmt.Errorf("release name is required")
		return h
	}

	h.helm = h.command()
	h.lastResult, h.lastError = h.helm("install", h.releaseName, h.chartPath, "--create-namespace")
	if h.lastError != nil {
		h.collectDiagnostics()
	}
	return h
}

// Upgrade upgrades the Helm release
func (h *HelmChart) Upgrade() *HelmChart {
	if h.releaseName == "" {
		h.lastError = fmt.Errorf("release name is required")
		return h
	}
	h.helm = h.command()

	h.lastResult, h.lastError = h.helm("upgrade", h.releaseName, h.chartPath)
	if h.lastError != nil {
		h.collectDiagnostics()
	}
	return h
}

// Delete deletes the Helm release
func (h *HelmChart) Delete() *HelmChart {
	if h.releaseName == "" {
		h.lastError = fmt.Errorf("release name is required")
		return h
	}

	h.lastResult, h.lastError = helm("delete", "--namespace", h.namespace, h.releaseName, "--wait=false")
	return h
}

// GetPod returns a Pod accessor for the current release
func (h *HelmChart) GetPod(selector string) *Pod {
	return &Pod{
		Metadata: Metadata{
			Namespace: h.namespace,
		},
		selector:    selector,
		helm:        h,
		colorOutput: h.colorOutput,
	}
}

// GetStatefulSet returns a StatefulSet accessor
func (h *HelmChart) GetStatefulSet(name string) *StatefulSet {
	return &StatefulSet{
		name:        name,
		namespace:   h.namespace,
		helm:        h,
		colorOutput: h.colorOutput,
	}
}

// GetSecret returns a Secret accessor
func (h *HelmChart) GetSecret(name string) *Secret {
	return &Secret{
		name:        name,
		namespace:   h.namespace,
		helm:        h,
		colorOutput: h.colorOutput,
	}
}

// GetConfigMap returns a ConfigMap accessor
func (h *HelmChart) GetConfigMap(name string) *ConfigMap {
	return &ConfigMap{
		name:        name,
		namespace:   h.namespace,
		helm:        h,
		colorOutput: h.colorOutput,
	}
}

// GetPVC returns a PersistentVolumeClaim accessor
func (h *HelmChart) GetPVC(name string) *PVC {
	return &PVC{
		Metadata: Metadata{
			Name:      name,
			Namespace: h.namespace,
		},
		helm:        h,
		colorOutput: h.colorOutput,
	}
}

// Status returns the Helm release status
func (h *HelmChart) Status() (string, error) {
	result, err := helm("status", h.releaseName, "--namespace", h.namespace)
	return result.Stdout, err
}

// Error returns the last error
func (h *HelmChart) Error() error {
	return h.lastError
}

// Result returns the last command result
func (h *HelmChart) Result() *clickyExec.ExecResult {
	return h.lastResult
}

// MustSucceed panics if there was an error
func (h *HelmChart) MustSucceed() *HelmChart {
	if h.lastError != nil {
		panic(h.lastError)
	}
	return h
}

func (h *HelmChart) Matches(o Object) bool {
	if release, ok := o.Annotations["meta.helm.sh/release-name"]; !ok || release != h.releaseName {
		return false
	}
	if namespace, ok := o.Annotations["meta.helm.sh/release-namespace"]; !ok || namespace != h.namespace {
		return false
	}
	return true
}

// StatefulSet represents a Kubernetes StatefulSet
type StatefulSet struct {
	name        string
	namespace   string
	helm        *HelmChart
	colorOutput bool
	lastResult  *clickyExec.ExecResult
	lastError   error
}

// WaitReady waits for the StatefulSet to be ready
func (s *StatefulSet) WaitReady() *StatefulSet {
	return s.WaitFor(2 * time.Minute)
}

// WaitFor waits for the StatefulSet rollout to complete
func (s *StatefulSet) WaitFor(timeout time.Duration) *StatefulSet {
	args := []any{"rollout", "status", "statefulset", s.name,
		"-n", s.namespace, "--timeout=" + timeout.String()}

	s.lastResult, s.lastError = kubectl(args...)
	return s
}

// GetReplicas returns the number of ready replicas
func (s *StatefulSet) GetReplicas() (int, error) {
	args := []any{"get", "statefulset", s.name, "-n", s.namespace,
		"-o", "jsonpath={.status.readyReplicas}"}
	p, err := kubectl(args...)
	if err != nil {
		return 0, err
	}

	i, err := strconv.Atoi(p.Stdout)
	if err != nil {
		return 0, err
	}
	return i, nil
}

// GetGeneration returns the current generation
func (s *StatefulSet) GetGeneration() (int64, error) {
	args := []any{"get", "statefulset", s.name, "-n", s.namespace,
		"-o", "jsonpath={.metadata.generation}"}
	p, err := kubectl(args...)
	if err != nil {
		return 0, err
	}
	i, err := strconv.Atoi(p.Stdout)
	if err != nil {
		return 0, err
	}
	return int64(i), nil
}

// Secret represents a Kubernetes Secret
type Secret struct {
	name        string
	namespace   string
	helm        *HelmChart
	colorOutput bool
	lastResult  command.Result
}

// Get retrieves a secret value by key
func (s *Secret) Get(key string) (string, error) {
	args := []any{"get", "secret", s.name, "-n", s.namespace,
		"-o", fmt.Sprintf("jsonpath={.data.%s}", key)}
	p, err := kubectl(args...)
	if err != nil {
		return "", err
	}
	decoded, _ := base64.Decode(p.Stdout)

	return string(decoded), nil
}

// ConfigMap represents a Kubernetes ConfigMap
type ConfigMap struct {
	name        string
	namespace   string
	helm        *HelmChart
	colorOutput bool
	lastResult  *clickyExec.ExecResult
}

// Get retrieves a ConfigMap value by key
func (c *ConfigMap) Get(key string) (string, error) {
	escapedKey := strings.ReplaceAll(key, ".", "\\.")
	args := []any{"get", "configmap", c.name, "-n", c.namespace,
		"-o", fmt.Sprintf("jsonpath={.data['%s']}", escapedKey)}
	p, err := kubectl(args...)
	return p.Stdout, err
}

// PVC represents a PersistentVolumeClaim
type PVC struct {
	Metadata    `json:",inline"`
	helm        *HelmChart
	colorOutput bool
	lastResult  *clickyExec.ExecResult
}

// Status returns the PVC status
func (p *PVC) Status() (map[string]interface{}, error) {
	p.lastResult, _ = kubectl("get", "pvc", p.Name, "-n", p.Namespace, "-o", "json")
	if !p.lastResult.IsOk() {
		return nil, p.lastResult.Error
	}

	var pvc map[string]interface{}
	if err := json.Unmarshal([]byte(p.lastResult.Stdout), &pvc); err != nil {
		return nil, fmt.Errorf("failed to unmarshal PVC: %w", err)
	}

	return pvc, nil
}

// Helper methods

func (h *HelmChart) command(args ...string) Helm {

	if h.namespace != "" {
		args = append(args, "--namespace", h.namespace)
	}
	if h.wait {
		args = append(args, "--wait")
	}
	if h.timeout > 0 {
		args = append(args, "--timeout", h.timeout.String())
	}

	if h.dryRun {
		args = append(args, "--dry-run")
	}

	// Add values if any
	if len(h.values) > 0 {
		valuesYaml, err := yaml.Marshal(h.values)
		if err != nil {
			h.lastError = fmt.Errorf("failed to marshal values: %w", err)
			return nil
		}

		// Write values to temp file
		tempFile := fmt.Sprintf("/tmp/helm-values-%d.yaml", time.Now().UnixNano())
		cmd := exec.Command("sh", "-c", fmt.Sprintf("cat > %s", tempFile))
		cmd.Stdin = bytes.NewReader(valuesYaml)
		if err := cmd.Run(); err != nil {
			h.lastError = fmt.Errorf("failed to write values file: %w", err)
			return nil
		}

		args = append(args, "--values", tempFile)
		// Note: In production, should defer cleanup of temp file
	}

	cmd := clicky.Exec("helm", args...)

	return cmd.AsWrapper()
}

func (h *HelmChart) collectDiagnostics() {
	if !h.colorOutput {
		return
	}

	helm(clickyExec.WithDebug(), "status", h.releaseName, "-n", h.namespace)

	kubectl(clickyExec.WithDebug(), "get", "pods", "-n", h.namespace, "-o", "wide")

	kubectl(clickyExec.WithDebug(), "get", "events", "-n", h.namespace,
		"--sort-by=.lastTimestamp")
}
