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
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/confighub/sdk/core/worker/api"
	"github.com/confighub/sdk/core/workerapi"
)

const ProviderVMCluster = api.ProviderType("VMCluster")

// VMClusterBridge implements the ConfigHub BridgeWorker interface for provisioning
// single-node k3s clusters on EC2.
type VMClusterBridge struct {
	awsCfg            aws.Config
	roleARN           string
	hostedZoneID      string
	subnetID          string
	instanceProfileID string

	// ConfigHub API credentials (this worker's own credentials, used to manage child workers)
	confighubURL    string
	confighubID     string
	confighubSecret string
}

type VMClusterBridgeConfig struct {
	// RoleARN is the IAM role to assume in the demo account.
	RoleARN string
	// HostedZoneID is the Route53 hosted zone for DNS records.
	HostedZoneID string
	// SubnetID is the VPC subnet to launch instances in.
	SubnetID string
	// InstanceProfileName is the IAM instance profile for EC2 instances (SSM access).
	InstanceProfileName string
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
		awsCfg:            awsCfg,
		roleARN:           cfg.RoleARN,
		hostedZoneID:      cfg.HostedZoneID,
		subnetID:          cfg.SubnetID,
		instanceProfileID: cfg.InstanceProfileName,
		confighubURL:      cfg.ConfigHubURL,
		confighubID:       cfg.ConfigHubID,
		confighubSecret:   cfg.ConfigHubSecret,
	}, nil
}

// assumeRoleConfig returns an AWS config with the cross-account role assumed.
func (b *VMClusterBridge) assumeRoleConfig(ctx context.Context) (aws.Config, error) {
	if b.roleARN == "" {
		// No cross-account role; use default credentials (for local dev).
		return b.awsCfg, nil
	}
	stsClient := sts.NewFromConfig(b.awsCfg)
	creds := stscreds.NewAssumeRoleProvider(stsClient, b.roleARN)
	cfg := b.awsCfg.Copy()
	cfg.Credentials = aws.NewCredentialsCache(creds)
	return cfg, nil
}

func (b *VMClusterBridge) ec2Client(ctx context.Context, region string) (*ec2.Client, error) {
	cfg, err := b.assumeRoleConfig(ctx)
	if err != nil {
		return nil, err
	}
	return ec2.NewFromConfig(cfg, func(o *ec2.Options) {
		if region != "" {
			o.Region = region
		}
	}), nil
}

func (b *VMClusterBridge) route53Client(ctx context.Context) (*route53.Client, error) {
	cfg, err := b.assumeRoleConfig(ctx)
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
				},
				AvailableTargets: []api.Target{
					{
						Name: api.GenerateTargetName(opts.WorkerSlug, ProviderVMCluster, workerapi.ToolchainKubernetesYAML, "default"),
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
