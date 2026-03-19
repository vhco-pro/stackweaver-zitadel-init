// Copyright (c) 2025 VH & Co BV. Licensed under the Business Source License 1.1. See LICENSE for details.

package zitadel

import (
	"fmt"

	orgV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	projectV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/project/v2"
)

// GetOrCreateOrg finds or creates a Zitadel organization by name.
func (c *Client) GetOrCreateOrg(name string) (string, error) {
	// Try to find existing org by name using v2
	listResp, err := c.orgService.ListOrganizations(c.ctx, &orgV2.ListOrganizationsRequest{
		Queries: []*orgV2.SearchQuery{
			{
				Query: &orgV2.SearchQuery_NameQuery{
					NameQuery: &orgV2.OrganizationNameQuery{
						Name: name,
					},
				},
			},
		},
	})
	if err == nil && listResp != nil && len(listResp.GetResult()) > 0 {
		orgID := listResp.GetResult()[0].GetId()
		if orgID != "" {
			c.orgID = orgID
			fmt.Printf("✅ Using existing organization: %s\n", orgID)
			return orgID, nil
		}
	}

	// Create new org using v2
	createResp, err := c.orgService.AddOrganization(c.ctx, &orgV2.AddOrganizationRequest{
		Name: name,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create org: %w", err)
	}

	orgID := createResp.GetOrganizationId()
	c.orgID = orgID
	fmt.Printf("✅ Created organization: %s\n", orgID)
	return orgID, nil
}

// GetOrCreateProject finds or creates a Zitadel project by name.
func (c *Client) GetOrCreateProject(orgID, name string) (string, error) {
	// Try to find existing project
	listResp, err := c.projectService.ListProjects(c.ctx, &projectV2.ListProjectsRequest{
		Filters: []*projectV2.ProjectSearchFilter{
			{
				Filter: &projectV2.ProjectSearchFilter_ProjectNameFilter{
					ProjectNameFilter: &projectV2.ProjectNameFilter{
						ProjectName: name,
					},
				},
			},
		},
	})
	if err == nil && listResp != nil && len(listResp.GetProjects()) > 0 {
		projectID := listResp.GetProjects()[0].GetProjectId()
		fmt.Printf("✅ Using existing project: %s\n", projectID)
		return projectID, nil
	}

	// Create new project
	createResp, err := c.projectService.CreateProject(c.ctx, &projectV2.CreateProjectRequest{
		OrganizationId: orgID,
		Name:           name,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create project: %w", err)
	}

	projectID := createResp.GetProjectId()
	fmt.Printf("✅ Created project: %s\n", projectID)

	return projectID, nil
}
