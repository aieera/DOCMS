package testutil

import (
	"context"
	"fmt"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// NewOpenSearchContainer starts a single-node OpenSearch 2.12 (the
// stack's pinned version) with the security plugin disabled, and returns
// its HTTP URL. Heap is capped at 512m — index/search integration tests
// need correctness, not capacity.
func NewOpenSearchContainer(ctx context.Context) (url string, cleanup Cleanup, err error) {
	req := testcontainers.ContainerRequest{
		Image:        "opensearchproject/opensearch:2.12.0",
		ExposedPorts: []string{"9200/tcp"},
		Env: map[string]string{
			"discovery.type":              "single-node",
			"DISABLE_SECURITY_PLUGIN":     "true",
			"DISABLE_INSTALL_DEMO_CONFIG": "true",
			"OPENSEARCH_JAVA_OPTS":        "-Xms512m -Xmx512m",
		},
		WaitingFor: wait.ForHTTP("/_cluster/health").WithPort("9200/tcp").
			WithStartupTimeout(120 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req, Started: true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("start opensearch: %w", err)
	}
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "9200")
	return fmt.Sprintf("http://%s:%s", host, port.Port()),
		func() { _ = c.Terminate(context.Background()) }, nil
}
