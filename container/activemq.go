package container

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/queue-exporter/pkg/activemq"
)

// ActiveMQContainer provides specialized ActiveMQ container management
type ActiveMQContainer struct {
	*Container
	username   string
	password   string
	brokerURL  string
	webConsoleURL string
}

// NewActiveMQ creates a new ActiveMQ container
func NewActiveMQ(name, username, password string, reuse bool) (*ActiveMQContainer, error) {
	// Note: Logging will be available after container creation

	if username == "" {
		username = "admin"
	}
	if password == "" {
		password = "admin"
	}

	// Create temporary directories for ActiveMQ data and configuration
	tempDataDir, err := os.MkdirTemp("", "activemq-data-"+name+"-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp data directory: %w", err)
	}

	tempConfDir, err := os.MkdirTemp("", "activemq-conf-"+name+"-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp conf directory: %w", err)
	}

	// Find configuration files by walking up the directory tree
	var fixturesDir string
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}

	// Search for test/fixtures directory by walking up from current directory
	currentDir := cwd
	for {
		testPath := filepath.Join(currentDir, "test", "fixtures")
		if stat, err := os.Stat(testPath); err == nil && stat.IsDir() {
			fixturesDir = testPath
			break
		}

		// Move up one directory
		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			// Reached filesystem root
			break
		}
		currentDir = parentDir
	}

	if fixturesDir == "" {
		return nil, fmt.Errorf("test/fixtures directory not found from %s", cwd)
	}

	// Copy configuration files, but we'll only mount activemq.xml and let the container
	// use its default jetty.xml with an environment variable override for host binding
	activemqXmlSrc := filepath.Join(fixturesDir, "activemq.xml")
	activemqXmlDest := filepath.Join(tempConfDir, "activemq.xml")

	if err := copyFile(activemqXmlSrc, activemqXmlDest); err != nil {
		return nil, fmt.Errorf("failed to copy activemq.xml: %w", err)
	}

	config := Config{
		Image: "apache/activemq-classic:5.18.7",
		Name:  name,
		Ports: map[string]string{
			"61616": "0", // OpenWire broker port
			"8161":  "0", // Web console port
			"1099":  "0", // JMX monitoring port
		},
		Env: []string{
			fmt.Sprintf("ACTIVEMQ_ADMIN_LOGIN=%s", username),
			fmt.Sprintf("ACTIVEMQ_ADMIN_PASSWORD=%s", password),
			"ACTIVEMQ_OPTS=-Xms256m -Xmx512m -XX:+UseG1GC -XX:MaxGCPauseMillis=200 -Dcom.sun.management.jmxremote -Dcom.sun.management.jmxremote.port=1099 -Dcom.sun.management.jmxremote.local.only=false -Dcom.sun.management.jmxremote.authenticate=false -Dcom.sun.management.jmxremote.ssl=false -Djetty.host=0.0.0.0",
		},
		Mounts: []Mount{
			{
				Source: activemqXmlDest,
				Target: "/opt/apache-activemq/conf/activemq.xml",
				Type:   "bind",
				ReadOnly: true,
			},
			{
				Source: tempDataDir,
				Target: "/opt/apache-activemq/data",
				Type:   "bind",
			},
		},
		Reuse: reuse,
	}

	container, err := New(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create ActiveMQ container: %w", err)
	}

	activemqContainer := &ActiveMQContainer{
		Container: container,
		username:  username,
		password:  password,
	}

	// Now we can log since we have the container instance
	activemqContainer.Infof("Creating new ActiveMQ container: reuse=%v", reuse)
	activemqContainer.Infof("Using credentials for user: %s", username)
	activemqContainer.Infof("Container config - Image: %s, Ports: %v", config.Image, config.Ports)
	activemqContainer.Infof("JVM Options: -Xms512m -Xmx1g -XX:+UseG1GC with JMX monitoring")

	return activemqContainer, nil
}

// Start starts the ActiveMQ container and waits for it to be ready
func (a *ActiveMQContainer) Start(ctx context.Context) error {
	a.Infof("Starting container")

	if err := a.Container.Start(ctx); err != nil {
		a.Container.PrintLogsOnFailure(ctx, fmt.Sprintf("Failed to start ActiveMQ container: %v", err))
		return fmt.Errorf("failed to start ActiveMQ container: %w", err)
	}

	a.Infof("Container started, retrieving port mappings...")

	// Get the host ports
	brokerPort, err := a.GetPort("61616")
	if err != nil {
		a.Errorf("Failed to get broker port mapping: %v", err)
		return fmt.Errorf("failed to get ActiveMQ broker port: %w", err)
	}

	webPort, err := a.GetPort("8161")
	if err != nil {
		a.Errorf("Failed to get web console port mapping: %v", err)
		return fmt.Errorf("failed to get ActiveMQ web console port: %w", err)
	}

	jmxPort, err := a.GetPort("1099")
	if err != nil {
		a.Errorf("Failed to get JMX port mapping: %v", err)
		return fmt.Errorf("failed to get ActiveMQ JMX port: %w", err)
	}

	// Build URLs
	a.brokerURL = fmt.Sprintf("tcp://localhost:%s", brokerPort)
	a.webConsoleURL = fmt.Sprintf("http://localhost:%s", webPort)

	a.Infof("Port mappings - Broker: %s, Web Console: %s, JMX: %s",
		brokerPort, webPort, jmxPort)
	a.Infof("Service URLs - Broker: %s, Web Console: %s",
		a.brokerURL, a.webConsoleURL)

	// Wait for ActiveMQ to be ready
	a.Infof("Waiting for ActiveMQ to become ready...")
	err = a.waitForReady(ctx)
	if err != nil {
		a.Container.PrintLogsOnFailure(ctx, fmt.Sprintf("ActiveMQ failed to become ready: %v", err))
		return err
	}

	// Run comprehensive health check after startup
	a.Infof("Running post-startup health check...")
	if err := a.HealthCheck(); err != nil {
		a.Container.PrintLogsOnFailure(ctx, fmt.Sprintf("ActiveMQ health check failed after startup: %v", err))
		return fmt.Errorf("ActiveMQ health check failed: %w", err)
	}

	a.Infof("ActiveMQ container is ready and accepting connections")
	return nil
}

// GetBrokerURL returns the broker URL for client connections
func (a *ActiveMQContainer) GetBrokerURL() string {
	return a.brokerURL
}

// GetTCPBrokerURL returns the TCP broker URL for OpenWire connections
func (a *ActiveMQContainer) GetTCPBrokerURL() string {
	return a.brokerURL
}

// GetJMXPort returns the JMX monitoring port
func (a *ActiveMQContainer) GetJMXPort() (string, error) {
	return a.GetPort("1099")
}

// GetWebConsoleURL returns the web console URL
func (a *ActiveMQContainer) GetWebConsoleURL() string {
	return a.webConsoleURL
}

// GetCredentials returns the username and password
func (a *ActiveMQContainer) GetCredentials() (string, string) {
	return a.username, a.password
}

// CreateClient creates a new ActiveMQ client connected to this container
func (a *ActiveMQContainer) CreateClient() *activemq.Client {
	return activemq.NewClient(a.webConsoleURL, a.username, a.password, "localhost")
}

// waitForReady waits for ActiveMQ to be ready to accept connections
func (a *ActiveMQContainer) waitForReady(ctx context.Context) error {
	maxRetries := 60  // ActiveMQ can take longer to start than SQL Server
	retryDelay := 2 * time.Second

	a.Infof("Starting readiness check (max %d attempts, %v between attempts)", maxRetries, retryDelay)

	for i := 0; i < maxRetries; i++ {
		select {
		case <-ctx.Done():
			a.Infof("Context cancelled during readiness check")
			return ctx.Err()
		default:
		}

		a.Infof("Readiness check attempt %d/%d", i+1, maxRetries)

		if a.testConnection() {
			a.Infof("Readiness check passed after %d attempts", i+1)
			return nil
		}

		// If we've failed several attempts, print logs for debugging
		if i > 0 && (i+1)%10 == 0 {
			a.Infof("Printing container logs for debugging after %d failed attempts", i+1)
			a.Container.PrintLogsOnFailure(ctx, fmt.Sprintf("ActiveMQ readiness check failing after %d attempts", i+1))
		}

		if i < maxRetries-1 {
			a.Infof("Readiness check failed, waiting %v before retry...", retryDelay)
			time.Sleep(retryDelay)
		}
	}

	a.Errorf("Readiness check failed after %d attempts", maxRetries)
	a.Container.PrintLogsOnFailure(ctx, fmt.Sprintf("ActiveMQ readiness check failed after %d attempts", maxRetries))
	return fmt.Errorf("ActiveMQ failed to become ready after %d attempts", maxRetries)
}

// testConnection tests if ActiveMQ is ready using web console health check
func (a *ActiveMQContainer) testConnection() bool {
	if a.webConsoleURL == "" {
		a.Errorf("Web console URL not set - container may not be properly started")
		return false
	}

	a.Infof("Testing web console health: %s", a.webConsoleURL)

	client := &http.Client{
		Timeout: 3 * time.Second,
	}

	// Test without credentials - should return 401 if service is up and running
	req, err := http.NewRequest("GET", a.webConsoleURL+"/", nil)
	if err != nil {
		a.Infof("Failed to create web console request: %v", err)
		return false
	}

	resp, err := client.Do(req)
	if err != nil {
		a.Infof("Web console request failed: %v", err)
		return false
	}
	defer resp.Body.Close()

	a.Infof("Web console response status: %d", resp.StatusCode)

	// Accept 401 (unauthorized), 200 (success), or 302 (redirect) as indication of readiness
	success := resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusOK ||
		resp.StatusCode == http.StatusFound

	if success {
		a.Infof("Web console health check successful - service is running")
	} else {
		a.Infof("Web console health check failed with unexpected status %d", resp.StatusCode)
	}

	return success
}

// HealthCheck performs a comprehensive health check
func (a *ActiveMQContainer) HealthCheck() error {
	if a.webConsoleURL == "" {
		return fmt.Errorf("web console URL not set - container may not be started")
	}

	// Test web console connection without credentials - should return 401 if service is up
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest("GET", a.webConsoleURL+"/", nil)
	if err != nil {
		return fmt.Errorf("health check failed - cannot create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed - web console request failed: %w", err)
	}
	defer resp.Body.Close()

	// Accept 401 (unauthorized), 200 (success), or 302 (redirect) as indication of health
	if resp.StatusCode != http.StatusUnauthorized &&
		resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusFound {
		return fmt.Errorf("health check failed - web console returned unexpected status %d", resp.StatusCode)
	}

	// Test ActiveMQ client connection
	activemqClient := a.CreateClient()
	if activemqClient == nil {
		return fmt.Errorf("health check failed - cannot create ActiveMQ client")
	}

	// Try to list queues as a simple connectivity test
	_, err = activemqClient.ListQueues()
	if err != nil {
		return fmt.Errorf("health check failed - cannot list queues: %w", err)
	}

	return nil
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}