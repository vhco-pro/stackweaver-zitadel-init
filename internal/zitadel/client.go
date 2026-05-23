// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/zitadel/zitadel-go/v3/pkg/client"
	actionV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/action/v2"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/admin"
	applicationV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/application/v2"
	featureV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/feature/v2"
	internalpermission "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/internal_permission/v2"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/management"
	orgV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	projectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
	userV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
	zitadelpkg "github.com/zitadel/zitadel-go/v3/pkg/zitadel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	maxWait      = 300 * time.Second
	waitInterval = 2 * time.Second
)

// Client wraps Zitadel's gRPC service clients for provisioning.
type Client struct {
	ctx                context.Context
	api                *client.Client
	orgService         orgV2.OrganizationServiceClient
	projectService     projectV2.ProjectServiceClient
	applicationService applicationV2.ApplicationServiceClient
	userService        userV2.UserServiceClient
	internalPermission internalpermission.InternalPermissionServiceClient
	mgmtService        management.ManagementServiceClient
	adminService       admin.AdminServiceClient
	actionService      actionV2.ActionServiceClient
	featureService     featureV2.FeatureServiceClient
	internalAddr       string // host:port for HTTP calls (e.g. "localhost:8080")
	domain             string // ExternalDomain for Host header (e.g. "sw-auth.example.com")
	orgID              string
}

// NewClient creates a new Zitadel gRPC client.
func NewClient(accessToken, dialAddr, domain string) (*Client, error) {
	ctx := context.Background()

	// domain must match Zitadel's ExternalDomain so the gRPC :authority header
	// resolves to the correct instance. Docker Compose uses "localhost"; Kubernetes
	// passes the ingress auth hostname via ZITADEL_DOMAIN.
	if domain == "" {
		domain = "localhost"
	}
	zitadelInstance := zitadelpkg.New(domain, zitadelpkg.WithInsecure("8080"))

	if dialAddr == "" {
		dialAddr = "internal-zitadel:8080"
	}

	// Create a custom dialer that connects to the Docker network alias
	customDialer := func(ctx context.Context, addr string) (net.Conn, error) {
		dialer := &net.Dialer{}
		return dialer.DialContext(ctx, "tcp", dialAddr)
	}

	// Create client - SDK uses localhost for validation
	// Custom dialer connects to the provided alias
	api, err := client.New(ctx, zitadelInstance,
		client.WithAuth(client.PAT(accessToken)),
		client.WithGRPCDialOptions(
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(customDialer),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create Zitadel client: %w", err)
	}

	return &Client{
		ctx:                ctx,
		api:                api,
		orgService:         api.OrganizationServiceV2(),
		projectService:     api.ProjectServiceV2(),
		applicationService: api.ApplicationServiceV2(),
		userService:        api.UserServiceV2(),
		internalPermission: api.InternalPermissionServiceV2(),
		mgmtService:        api.ManagementService(),
		adminService:       api.AdminService(),
		actionService:      api.ActionServiceV2(),
		featureService:     api.FeatureServiceV2(),
		internalAddr:       dialAddr,
		domain:             domain,
	}, nil
}

// WaitForReady polls the gRPC endpoint until Zitadel is reachable.
func WaitForReady(grpcAddr string) error {
	if grpcAddr == "" {
		grpcAddr = "internal-zitadel:8080"
	}
	fmt.Printf("⏳ Waiting for Zitadel gRPC to be ready at %s...\n", grpcAddr)

	start := time.Now()
	for time.Since(start) < maxWait {
		// Try to connect to the gRPC port
		conn, err := net.DialTimeout("tcp", grpcAddr, 2*time.Second)
		if err == nil {
			conn.Close()
			fmt.Println("✅ Zitadel gRPC is ready!")
			return nil
		}

		if time.Since(start).Seconds() > 0 && int(time.Since(start).Seconds())%10 == 0 {
			fmt.Printf("   Waiting for Zitadel... (%ds)\n", int(time.Since(start).Seconds()))
		}

		time.Sleep(waitInterval)
	}

	return fmt.Errorf("zitadel gRPC not ready after %v", maxWait)
}
