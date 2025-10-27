package container

import (
	"context"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ActiveMQ Container", func() {
	Describe("Container Creation", func() {
		Context("when creating with custom credentials", func() {
			It("should create container with correct configuration", func() {
				container, err := NewActiveMQ("test-activemq", "admin", "admin123", false)
				Expect(err).ToNot(HaveOccurred())
				Expect(container).ToNot(BeNil())

				By("Verifying basic configuration")
				config := container.Container.config
				Expect(config.Image).To(Equal("apache/activemq-classic:5.18.7"))
				Expect(config.Name).To(Equal("test-activemq"))
				Expect(config.Reuse).To(BeFalse())

				By("Verifying port configuration")
				Expect(config.Ports).To(HaveKey("61616")) // OpenWire
				Expect(config.Ports).To(HaveKey("8161"))  // Web console
				Expect(config.Ports).To(HaveKey("1099"))  // JMX

				By("Verifying environment variables")
				envMap := make(map[string]string)
				for _, env := range config.Env {
					if strings.Contains(env, "=") {
						parts := strings.SplitN(env, "=", 2)
						envMap[parts[0]] = parts[1]
					}
				}

				Expect(envMap["ACTIVEMQ_ADMIN_LOGIN"]).To(Equal("admin"))
				Expect(envMap["ACTIVEMQ_ADMIN_PASSWORD"]).To(Equal("admin123"))
				Expect(envMap["ACTIVEMQ_OPTS"]).To(ContainSubstring("-Xms256m"))
				Expect(envMap["ACTIVEMQ_OPTS"]).To(ContainSubstring("-Xmx512m"))
				Expect(envMap["ACTIVEMQ_OPTS"]).To(ContainSubstring("-XX:+UseG1GC"))
				Expect(envMap["ACTIVEMQ_OPTS"]).To(ContainSubstring("jmxremote"))

				By("Verifying credentials")
				username, password := container.GetCredentials()
				Expect(username).To(Equal("admin"))
				Expect(password).To(Equal("admin123"))
			})
		})

		Context("when creating with default credentials", func() {
			It("should create container with default admin credentials", func() {
				container, err := NewActiveMQ("test-activemq-defaults", "", "", true)
				Expect(err).ToNot(HaveOccurred())
				Expect(container).ToNot(BeNil())

				By("Verifying default credentials")
				username, password := container.GetCredentials()
				Expect(username).To(Equal("admin"))
				Expect(password).To(Equal("admin"))

				By("Verifying reuse setting")
				Expect(container.Container.config.Reuse).To(BeTrue())
			})
		})
	})

	Describe("Container Methods", func() {
		Context("when container is not started", func() {
			It("should handle method calls gracefully", func() {
				container, err := NewActiveMQ("test-methods", "user", "pass", false)
				Expect(err).ToNot(HaveOccurred())

				By("Testing URL methods before starting")
				Expect(container.GetBrokerURL()).To(BeEmpty())
				Expect(container.GetTCPBrokerURL()).To(BeEmpty())
				Expect(container.GetWebConsoleURL()).To(BeEmpty())

				By("Testing JMX port before starting")
				_, err = container.GetJMXPort()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("container not started"))
			})
		})
	})

	Describe("Integration Tests", func() {
		BeforeEach(func() {
			if testing.Short() {
				Skip("Skipping integration tests")
			}
		})

		Context("when starting ActiveMQ container", func() {
			It("should start successfully and provide access to services", func() {
				container, err := NewActiveMQ("test-activemq-integration", "admin", "admin123", false)
				Expect(err).ToNot(HaveOccurred())
				defer container.Cleanup(context.Background())

				By("Starting the container")
				ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				defer cancel()

				err = container.Start(ctx)
				if err != nil {
					if strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
						Skip("Docker not available for integration test")
					}
					Expect(err).ToNot(HaveOccurred())
				}

				By("Verifying container is running")
				running, err := container.IsRunning(ctx)
				Expect(err).ToNot(HaveOccurred())
				Expect(running).To(BeTrue())

				By("Verifying URLs are set after starting")
				Expect(container.GetBrokerURL()).ToNot(BeEmpty())
				Expect(container.GetTCPBrokerURL()).ToNot(BeEmpty())
				Expect(container.GetWebConsoleURL()).ToNot(BeEmpty())
				Expect(container.GetBrokerURL()).To(ContainSubstring("tcp://localhost:"))
				Expect(container.GetWebConsoleURL()).To(ContainSubstring("http://localhost:"))

				By("Verifying JMX port is available")
				jmxPort, err := container.GetJMXPort()
				Expect(err).ToNot(HaveOccurred())
				Expect(jmxPort).ToNot(BeEmpty())

				By("Verifying broker URL and TCP broker URL are the same")
				Expect(container.GetBrokerURL()).To(Equal(container.GetTCPBrokerURL()))
			})
		})

		Context("when performing health checks", func() {
			It("should fail before starting and pass after starting", func() {
				container, err := NewActiveMQ("test-activemq-health", "admin", "admin123", false)
				Expect(err).ToNot(HaveOccurred())
				defer container.Cleanup(context.Background())

				By("Testing health check before starting")
				err = container.HealthCheck()
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("web console URL not set"))

				By("Starting the container")
				ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				defer cancel()

				err = container.Start(ctx)
				if err != nil {
					if strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
						Skip("Docker not available for integration test")
					}
					Expect(err).ToNot(HaveOccurred())
				}

				By("Testing health check after starting")
				err = container.HealthCheck()
				Expect(err).ToNot(HaveOccurred())
			})
		})

		Context("when accessing container ports", func() {
			It("should provide access to all configured ports", func() {
				container, err := NewActiveMQ("test-activemq-ports", "admin", "admin123", false)
				Expect(err).ToNot(HaveOccurred())
				defer container.Cleanup(context.Background())

				By("Starting the container")
				ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
				defer cancel()

				err = container.Start(ctx)
				if err != nil {
					if strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
						Skip("Docker not available for integration test")
					}
					Expect(err).ToNot(HaveOccurred())
				}

				By("Testing all ports are accessible")
				brokerPort, err := container.GetPort("61616")
				Expect(err).ToNot(HaveOccurred())
				Expect(brokerPort).ToNot(BeEmpty())

				webPort, err := container.GetPort("8161")
				Expect(err).ToNot(HaveOccurred())
				Expect(webPort).ToNot(BeEmpty())

				jmxPort, err := container.GetJMXPort()
				Expect(err).ToNot(HaveOccurred())
				Expect(jmxPort).ToNot(BeEmpty())

				By("Verifying all ports are different")
				Expect(brokerPort).ToNot(Equal(webPort))
				Expect(brokerPort).ToNot(Equal(jmxPort))
				Expect(webPort).ToNot(Equal(jmxPort))
			})
		})
	})
})
