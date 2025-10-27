package container

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestPodinfoContainer(t *testing.T) {
	ctx := context.Background()

	config := Config{
		Image: "ghcr.io/stefanprodan/podinfo:latest",
		Name:  "test-podinfo",
		Ports: map[string]string{
			"9898": "0",
		},
		Reuse: false,
	}

	container, err := New(config)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}

	defer func() {
		if err := container.Cleanup(ctx); err != nil {
			t.Logf("Failed to cleanup container: %v", err)
		}
	}()

	t.Run("Start container", func(t *testing.T) {
		if err := container.Start(ctx); err != nil {
			t.Fatalf("Failed to start container: %v", err)
		}
	})

	t.Run("Container is running", func(t *testing.T) {
		running, err := container.IsRunning(ctx)
		if err != nil {
			t.Fatalf("Failed to check if container is running: %v", err)
		}
		if !running {
			t.Fatal("Container should be running")
		}
	})

	t.Run("Get container ID", func(t *testing.T) {
		id := container.GetID()
		if id == "" {
			t.Fatal("Container ID should not be empty")
		}
	})

	var hostPort string
	t.Run("Get mapped port", func(t *testing.T) {
		var err error
		hostPort, err = container.GetPort("9898")
		if err != nil {
			t.Fatalf("Failed to get port: %v", err)
		}
		if hostPort == "" {
			t.Fatal("Host port should not be empty")
		}
		t.Logf("Podinfo is accessible on port %s", hostPort)
	})

	t.Run("HTTP endpoint is accessible", func(t *testing.T) {
		url := fmt.Sprintf("http://localhost:%s/healthz", hostPort)

		var resp *http.Response
		var err error
		for i := 0; i < 10; i++ {
			resp, err = http.Get(url)
			if err == nil && resp.StatusCode == http.StatusOK {
				break
			}
			time.Sleep(time.Second)
		}

		if err != nil {
			t.Fatalf("Failed to reach podinfo endpoint: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected status 200, got %d", resp.StatusCode)
		}
	})

	t.Run("Get container logs", func(t *testing.T) {
		logs, err := container.Logs(ctx, false)
		if err != nil {
			t.Fatalf("Failed to get logs: %v", err)
		}
		defer logs.Close()

		logContent, err := io.ReadAll(logs)
		if err != nil {
			t.Fatalf("Failed to read logs: %v", err)
		}

		if len(logContent) == 0 {
			t.Fatal("Log content should not be empty")
		}
	})

	t.Run("Execute command in container", func(t *testing.T) {
		output, err := container.Exec(ctx, []string{"ls", "/"})
		if err != nil {
			t.Fatalf("Failed to execute command: %v", err)
		}
		if output == "" {
			t.Fatal("Command output should not be empty")
		}
	})

	t.Run("Stop container", func(t *testing.T) {
		if err := container.Stop(ctx); err != nil {
			t.Fatalf("Failed to stop container: %v", err)
		}

		running, err := container.IsRunning(ctx)
		if err != nil {
			t.Fatalf("Failed to check if container is running: %v", err)
		}
		if running {
			t.Fatal("Container should not be running after stop")
		}
	})
}
