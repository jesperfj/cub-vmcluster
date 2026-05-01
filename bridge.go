package main

import (
	"context"
	"fmt"
	"log"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/confighub/sdk/core/worker/api"
	"github.com/confighub/sdk/core/workerapi"
)

const ProviderVMCluster = api.ProviderType("VMCluster")

// VMClusterBridge implements the ConfigHub BridgeWorker interface for provisioning
// single-node k3s clusters on EC2.
type VMClusterBridge struct {
	awsCfg aws.Config

	// ConfigHub API credentials (this worker's own credentials, used to manage child workers)
	confighubURL    string
	confighubID     string
	confighubSecret string
}

type VMClusterBridgeConfig struct {
	// ConfigHub API credentials for managing child workers.
	ConfigHubURL    string
	ConfigHubID     string
	ConfigHubSecret string
}

func NewVMClusterBridge(cfg VMClusterBridgeConfig) (*VMClusterBridge, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &VMClusterBridge{
		awsCfg:          awsCfg,
		confighubURL:    cfg.ConfigHubURL,
		confighubID:     cfg.ConfigHubID,
		confighubSecret: cfg.ConfigHubSecret,
	}, nil
}

// assumeRoleConfig returns an AWS config with the cross-account role assumed.
// roleARN may be empty for local dev (uses default credentials).
func (b *VMClusterBridge) assumeRoleConfig(ctx context.Context, roleARN string) (aws.Config, error) {
	if roleARN == "" {
		return b.awsCfg, nil
	}
	stsClient := sts.NewFromConfig(b.awsCfg)
	creds := stscreds.NewAssumeRoleProvider(stsClient, roleARN)
	cfg := b.awsCfg.Copy()
	cfg.Credentials = aws.NewCredentialsCache(creds)
	return cfg, nil
}

func (b *VMClusterBridge) ec2Client(ctx context.Context, roleARN, region string) (*ec2.Client, error) {
	cfg, err := b.assumeRoleConfig(ctx, roleARN)
	if err != nil {
		return nil, err
	}
	return ec2.NewFromConfig(cfg, func(o *ec2.Options) {
		if region != "" {
			o.Region = region
		}
	}), nil
}

func (b *VMClusterBridge) ssmClient(ctx context.Context, roleARN, region string) (*ssm.Client, error) {
	cfg, err := b.assumeRoleConfig(ctx, roleARN)
	if err != nil {
		return nil, err
	}
	return ssm.NewFromConfig(cfg, func(o *ssm.Options) {
		if region != "" {
			o.Region = region
		}
	}), nil
}

func (b *VMClusterBridge) route53Client(ctx context.Context, roleARN string) (*route53.Client, error) {
	cfg, err := b.assumeRoleConfig(ctx, roleARN)
	if err != nil {
		return nil, err
	}
	// Route53 is global but the SDK still needs a region for the endpoint.
	// Force us-east-1 to avoid failures when the default region isn't set
	// (e.g., in-cluster pods without AWS_REGION).
	return route53.NewFromConfig(cfg, func(o *route53.Options) {
		o.Region = "us-east-1"
	}), nil
}

func (b *VMClusterBridge) ID() api.BridgeWorkerID {
	return api.BridgeWorkerID{
		ProviderType:   ProviderVMCluster,
		ToolchainTypes: []workerapi.ToolchainType{workerapi.ToolchainKubernetesYAML},
	}
}

func (b *VMClusterBridge) Info(opts api.InfoOptions) api.BridgeInfo {
	return api.BridgeInfo{
		SupportedConfigTypes: []*api.SupportedConfigType{
			{
				ConfigTypeSignature: api.ConfigTypeSignature{
					ConfigType: api.ConfigType{
						ToolchainType: workerapi.ToolchainKubernetesYAML,
						ProviderType:  ProviderVMCluster,
					},
					Options: []api.BridgeOption{
						{Name: "SubnetID", DataType: "string", Description: "VPC subnet to launch instances in", Required: true},
						{Name: "HostedZoneID", DataType: "string", Description: "Route53 hosted zone for DNS records (optional)"},
						{Name: "Region", DataType: "string", Description: "AWS region for the target", Required: true},
						{Name: "RoleARN", DataType: "string", Description: "Optional cross-account IAM role ARN to assume"},
					},
				},
			},
		},
	}
}

func (b *VMClusterBridge) Import(ctx api.BridgeContext, payload api.BridgePayload) error {
	log.Printf("[WARN] Import not implemented for VMCluster bridge")
	return fmt.Errorf("import not implemented")
}

func (b *VMClusterBridge) Finalize(ctx api.BridgeContext, payload api.BridgePayload) error {
	return nil
}

var _ api.Bridge = (*VMClusterBridge)(nil)
