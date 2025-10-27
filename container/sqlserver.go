package container

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/microsoft/go-mssqldb"
)

// SQLServerContainer provides specialized SQL Server container management
type SQLServerContainer struct {
	*Container
	password         string
	connectionString string
}

// NewSQLServer creates a new SQL Server container
func NewSQLServer(name, password string, reuse bool) (*SQLServerContainer, error) {
	config := Config{
		Image: "mcr.microsoft.com/azure-sql-edge:latest",
		Name:  name,
		Ports: map[string]string{"1433": "0"}, // Let Docker assign random port
		Env: []string{
			"ACCEPT_EULA=Y",
			fmt.Sprintf("SA_PASSWORD=%s", password),
			"MSSQL_PID=Developer",
		},
		Reuse: reuse,
	}

	container, err := New(config)
	if err != nil {
		return nil, err
	}

	return &SQLServerContainer{
		Container: container,
		password:  password,
	}, nil
}

// Start starts the SQL Server container and waits for it to be ready
func (s *SQLServerContainer) Start(ctx context.Context) error {
	if err := s.Container.Start(ctx); err != nil {
		return err
	}

	// Get the host port
	hostPort, err := s.GetPort("1433")
	if err != nil {
		return fmt.Errorf("failed to get SQL Server port: %w", err)
	}

	// Build connection string
	s.connectionString = fmt.Sprintf("server=localhost;port=%s;database=master;user id=sa;password=%s;encrypt=disable", hostPort, s.password)

	// Wait for SQL Server to be ready
	return s.waitForReady(ctx)
}

// GetConnectionString returns the JDBC connection string
func (s *SQLServerContainer) GetConnectionString() string {
	return s.connectionString
}

// waitForReady waits for SQL Server to be ready to accept connections
func (s *SQLServerContainer) waitForReady(ctx context.Context) error {
	maxRetries := 30
	retryDelay := 2 * time.Second

	for i := 0; i < maxRetries; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if s.testConnection() {
			return nil
		}

		if i < maxRetries-1 {
			time.Sleep(retryDelay)
		}
	}

	return fmt.Errorf("SQL Server failed to become ready after %d attempts", maxRetries)
}

// testConnection tests if SQL Server is ready
func (s *SQLServerContainer) testConnection() bool {
	db, err := sql.Open("sqlserver", s.connectionString)
	if err != nil {
		return false
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return false
	}

	// Test with a simple query
	var result int
	if err := db.QueryRow("SELECT 1").Scan(&result); err != nil {
		return false
	}

	return result == 1
}

// HealthCheck performs a comprehensive health check
func (s *SQLServerContainer) HealthCheck() error {
	if s.connectionString == "" {
		return fmt.Errorf("connection string not set - container may not be started")
	}

	db, err := sql.Open("sqlserver", s.connectionString)
	if err != nil {
		return fmt.Errorf("health check failed - cannot open database: %w", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return fmt.Errorf("health check failed - ping failed: %w", err)
	}

	// Test with a simple query
	var result int
	if err := db.QueryRow("SELECT 1").Scan(&result); err != nil {
		return fmt.Errorf("health check failed - query failed: %w", err)
	}

	if result != 1 {
		return fmt.Errorf("health check failed - unexpected query result: %d", result)
	}

	return nil
}
