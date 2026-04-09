package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/confighub/sdk/core/cubapi"
	goclientnew "github.com/confighub/sdk/core/openapi/goclient-new"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// ConfigHubClient wraps the generated API client with worker authentication.
type ConfigHubClient struct {
	client       *goclientnew.ClientWithResponses
	spaceIDCache map[string]openapi_types.UUID
}

// NewConfigHubClient authenticates as a worker and returns an API client.
func NewConfigHubClient(serverURL, workerID, workerSecret string) (*ConfigHubClient, error) {
	session, err := cubapi.PerformWorkerAuth(serverURL, workerID, workerSecret)
	if err != nil {
		return nil, fmt.Errorf("worker auth failed: %w", err)
	}

	client, err := goclientnew.NewClientWithResponses(serverURL+"/api",
		goclientnew.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", session.AccessToken))
			return nil
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}

	return &ConfigHubClient{
		client:       client,
		spaceIDCache: make(map[string]openapi_types.UUID),
	}, nil
}

// WorkerCredentials holds the ID and secret needed to start a cub-worker.
type WorkerCredentials struct {
	WorkerID string
	Secret   string
}

// EnsureWorker creates a worker if it doesn't exist, or returns the existing one.
// On create, the secret is returned. On existing, secret must come from LiveState.
// The worker is created with SupportedConfigTypes so that targets can be created against it
// before the worker connects.
func (c *ConfigHubClient) EnsureWorker(ctx context.Context, spaceSlug, workerSlug string) (*WorkerCredentials, bool, error) {
	spaceID, err := c.resolveSpaceID(ctx, spaceSlug)
	if err != nil {
		return nil, false, fmt.Errorf("failed to resolve space %q: %w", spaceSlug, err)
	}

	allowExists := "true"
	params := &goclientnew.CreateBridgeWorkerParams{
		AllowExists: &allowExists,
	}

	resp, err := c.client.CreateBridgeWorkerWithResponse(ctx, spaceID, params,
		goclientnew.CreateBridgeWorkerJSONRequestBody{
			Slug: workerSlug,
			ProvidedInfo: &goclientnew.WorkerInfo{
				BridgeWorkerInfo: &goclientnew.BridgeWorkerInfo{
					SupportedConfigTypes: []goclientnew.SupportedConfigType{
						{
							ProviderType:  "Kubernetes",
							ToolchainType: "Kubernetes/YAML",
						},
					},
				},
			},
		},
	)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create worker: %w", err)
	}

	if resp.JSON200 != nil {
		worker := resp.JSON200
		created := worker.Secret != ""
		return &WorkerCredentials{
			WorkerID: worker.BridgeWorkerID.String(),
			Secret:   worker.Secret,
		}, created, nil
	}

	return nil, false, fmt.Errorf("unexpected response status: %d %s", resp.StatusCode(), string(resp.Body))
}

// EnsureTarget creates a target if it doesn't exist, or returns the existing one.
func (c *ConfigHubClient) EnsureTarget(ctx context.Context, spaceSlug, targetSlug string, bridgeWorkerID openapi_types.UUID) (*goclientnew.Target, error) {
	spaceID, err := c.resolveSpaceID(ctx, spaceSlug)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve space %q: %w", spaceSlug, err)
	}

	allowExists := "true"
	params := &goclientnew.CreateTargetParams{
		AllowExists: &allowExists,
	}

	resp, err := c.client.CreateTargetWithResponse(ctx, spaceID, params,
		goclientnew.CreateTargetJSONRequestBody{
			Slug:           targetSlug,
			BridgeWorkerID: bridgeWorkerID,
			ProviderType:   "Kubernetes",
			ToolchainType:  "Kubernetes/YAML",
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create target: %w", err)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("unexpected response status: %d %s", resp.StatusCode(), string(resp.Body))
	}
	return resp.JSON200, nil
}

// EnsureConfigUnit creates a config unit if it doesn't exist, or returns the existing one.
func (c *ConfigHubClient) EnsureConfigUnit(ctx context.Context, spaceSlug, unitSlug string, targetID openapi_types.UUID, manifestData string) (*goclientnew.Unit, error) {
	spaceID, err := c.resolveSpaceID(ctx, spaceSlug)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve space %q: %w", spaceSlug, err)
	}

	allowExists := "true"
	params := &goclientnew.CreateUnitParams{
		AllowExists: &allowExists,
	}

	resp, err := c.client.CreateUnitWithResponse(ctx, spaceID, params,
		goclientnew.CreateUnitJSONRequestBody{
			Slug:          unitSlug,
			ToolchainType: "Kubernetes/YAML",
			TargetID:      &targetID,
			Data:          base64.StdEncoding.EncodeToString([]byte(manifestData)),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create config unit: %w", err)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("unexpected response status: %d %s", resp.StatusCode(), string(resp.Body))
	}
	return resp.JSON200, nil
}

// resolveSpaceID looks up a space by slug and returns its UUID. Results are cached.
func (c *ConfigHubClient) resolveSpaceID(ctx context.Context, spaceSlug string) (openapi_types.UUID, error) {
	if id, ok := c.spaceIDCache[spaceSlug]; ok {
		return id, nil
	}

	resp, err := c.client.ListSpacesWithResponse(ctx, &goclientnew.ListSpacesParams{})
	if err != nil {
		return openapi_types.UUID{}, err
	}
	if resp.JSON200 == nil {
		return openapi_types.UUID{}, fmt.Errorf("failed to list spaces: status %d, body: %s", resp.StatusCode(), string(resp.Body))
	}
	for _, es := range *resp.JSON200 {
		if es.Space != nil && es.Space.Slug == spaceSlug {
			c.spaceIDCache[spaceSlug] = es.Space.SpaceID
			return es.Space.SpaceID, nil
		}
	}
	return openapi_types.UUID{}, fmt.Errorf("space %q not found", spaceSlug)
}
