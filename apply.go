package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/confighub/sdk/core/worker/api"
	googleuuid "github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// ConfigHubSetupResult holds the resources created/resolved during setup.
type ConfigHubSetupResult struct {
	WorkerID     string
	WorkerSecret string
	TargetID     string
	ConfigUnitID string
	Manifest     string
}

func (b *VMClusterBridge) Apply(ctx api.BridgeContext, payload api.BridgePayload) error {
	startTime := time.Now()

	// Send initial status
	if err := ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:    api.ActionApply,
			Result:    api.ActionResultNone,
			Status:    api.ActionStatusProgressing,
			Message:   "Parsing VMCluster spec",
			StartedAt: startTime,
		},
	}); err != nil {
		return err
	}

	// Parse the VMCluster resource from payload
	var cluster VMCluster
	if err := yaml.Unmarshal(payload.Data, &cluster); err != nil {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("failed to parse VMCluster YAML: %v", err))
	}

	if cluster.Kind != "VMCluster" {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("unexpected kind %q, expected VMCluster", cluster.Kind))
	}

	// Check for existing instance in LiveState
	var existing LiveState
	if len(payload.LiveState) > 0 {
		if err := json.Unmarshal(payload.LiveState, &existing); err != nil {
			log.Printf("[WARN] Failed to parse existing LiveState: %v", err)
		}
	}

	if existing.InstanceID != "" {
		// Instance already exists — check if it's still running
		ec2c, err := b.ec2Client(ctx.Context(), cluster.Spec.Region)
		if err != nil {
			return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("failed to create EC2 client: %v", err))
		}

		desc, err := ec2c.DescribeInstances(ctx.Context(), &ec2.DescribeInstancesInput{
			InstanceIds: []string{existing.InstanceID},
		})
		if err == nil && len(desc.Reservations) > 0 && len(desc.Reservations[0].Instances) > 0 {
			inst := desc.Reservations[0].Instances[0]
			if inst.State.Name == ec2types.InstanceStateNameRunning {
				// Check if instance type needs to change
				currentType := string(inst.InstanceType)
				desiredType := cluster.Spec.InstanceType
				if desiredType == "" {
					desiredType = "t3.medium"
				}

				if currentType != desiredType {
					return b.resizeInstance(ctx, payload, startTime, ec2c, &cluster, &existing, desiredType)
				}

				// Ensure ConfigHub resources exist (idempotent)
				setup, err := b.setupConfigHubResources(ctx.Context(), ctx, payload, startTime, &cluster, &existing)
				if err != nil {
					return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("failed to setup ConfigHub resources: %v", err))
				}

				// Update LiveState with ConfigHub resource IDs
				existing.TargetID = setup.TargetID
				existing.ConfigUnitID = setup.ConfigUnitID
				if setup.WorkerSecret != "" {
					existing.WorkerSecret = setup.WorkerSecret
				}
				liveStateJSON, _ := json.Marshal(existing)

				// No changes — report synced
				terminatedAt := time.Now()
				return ctx.SendStatus(&api.ActionResult{
					UnitID:            payload.UnitID,
					SpaceID:           payload.SpaceID,
					QueuedOperationID: payload.QueuedOperationID,
					ActionResultBaseMeta: api.ActionResultMeta{
						Action:       api.ActionApply,
						Result:       api.ActionResultApplySynced,
						Status:       api.ActionStatusCompleted,
						Message:      fmt.Sprintf("Instance %s already running (%s)", existing.InstanceID, currentType),
						StartedAt:    startTime,
						TerminatedAt: &terminatedAt,
					},
					Data:      payload.Data,
					LiveData:  payload.Data,
					LiveState: liveStateJSON,
				})
			}
		}
	}

	// --- Provision new cluster ---
	awsCtx := ctx.Context()
	region := cluster.Spec.Region
	if region == "" {
		region = "us-east-1"
	}

	ec2c, err := b.ec2Client(awsCtx, region)
	if err != nil {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("failed to create EC2 client: %v", err))
	}

	// Step 1: Security Group
	_ = ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:    api.ActionApply,
			Result:    api.ActionResultNone,
			Status:    api.ActionStatusProgressing,
			Message:   "Creating security group",
			StartedAt: startTime,
		},
	})

	// Look up the VPC from the subnet so the security group is in the right network.
	vpcID, err := b.getSubnetVPC(awsCtx, ec2c)
	if err != nil {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("failed to resolve VPC from subnet: %v", err))
	}

	sgID, err := b.ensureSecurityGroup(awsCtx, ec2c, cluster.Metadata.Name, vpcID)
	if err != nil {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("failed to create security group: %v", err))
	}
	log.Printf("[INFO] Security group: %s", sgID)

	// Step 2: Setup ConfigHub resources (worker, target, config unit)
	setup, err := b.setupConfigHubResources(awsCtx, ctx, payload, startTime, &cluster, &existing)
	if err != nil {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("failed to setup ConfigHub resources: %v", err))
	}

	// Step 3: Render cloud-init
	userData, err := renderUserData(&cluster, setup.Manifest, b)
	if err != nil {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("failed to render cloud-init: %v", err))
	}

	// Step 4: Find latest Ubuntu AMI matching instance architecture
	amiID, err := b.findUbuntuAMI(awsCtx, ec2c, cluster.Spec.InstanceType)
	if err != nil {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("failed to find Ubuntu AMI: %v", err))
	}
	log.Printf("[INFO] Using AMI: %s", amiID)

	// Step 5: Launch instance
	_ = ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:    api.ActionApply,
			Result:    api.ActionResultNone,
			Status:    api.ActionStatusProgressing,
			Message:   "Launching EC2 instance",
			StartedAt: startTime,
		},
	})

	instanceType := ec2types.InstanceTypeT3Medium
	if cluster.Spec.InstanceType != "" {
		instanceType = ec2types.InstanceType(cluster.Spec.InstanceType)
	}

	diskSize := int32(30)
	if cluster.Spec.DiskSizeGB > 0 {
		diskSize = int32(cluster.Spec.DiskSizeGB)
	}

	runInput := &ec2.RunInstancesInput{
		ImageId:          aws.String(amiID),
		InstanceType:     instanceType,
		MinCount:         aws.Int32(1),
		MaxCount:         aws.Int32(1),
		UserData:         aws.String(base64.StdEncoding.EncodeToString([]byte(userData))),
		SecurityGroupIds: []string{sgID},
		BlockDeviceMappings: []ec2types.BlockDeviceMapping{
			{
				DeviceName: aws.String("/dev/sda1"),
				Ebs: &ec2types.EbsBlockDevice{
					VolumeSize: aws.Int32(diskSize),
					VolumeType: ec2types.VolumeTypeGp3,
				},
			},
		},
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeInstance,
				Tags: []ec2types.Tag{
					{Key: aws.String("Name"), Value: aws.String(fmt.Sprintf("vmcluster-%s", cluster.Metadata.Name))},
					{Key: aws.String("confighub:managed-by"), Value: aws.String("cub-vmcluster")},
					{Key: aws.String("confighub:cluster-name"), Value: aws.String(cluster.Metadata.Name)},
					{Key: aws.String("confighub:status"), Value: aws.String("launching")},
				},
			},
		},
		// IMDSv2 required, hop limit 2 so pods can reach IMDS
		MetadataOptions: &ec2types.InstanceMetadataOptionsRequest{
			HttpTokens:              ec2types.HttpTokensStateRequired,
			HttpEndpoint:            ec2types.InstanceMetadataEndpointStateEnabled,
			HttpPutResponseHopLimit: aws.Int32(2),
		},
	}

	if b.subnetID != "" {
		runInput.SubnetId = aws.String(b.subnetID)
	}
	if b.instanceProfileID != "" {
		runInput.IamInstanceProfile = &ec2types.IamInstanceProfileSpecification{
			Name: aws.String(b.instanceProfileID),
		}
	}

	runResult, err := ec2c.RunInstances(awsCtx, runInput)
	if err != nil {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("failed to launch instance: %v", err))
	}

	instance := runResult.Instances[0]
	instanceID := aws.ToString(instance.InstanceId)
	log.Printf("[INFO] Instance launched: %s", instanceID)

	// Step 6: Wait for instance running
	_ = ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:    api.ActionApply,
			Result:    api.ActionResultNone,
			Status:    api.ActionStatusProgressing,
			Message:   fmt.Sprintf("Waiting for instance %s to start", instanceID),
			StartedAt: startTime,
		},
	})

	waiter := ec2.NewInstanceRunningWaiter(ec2c)
	if err := waiter.Wait(awsCtx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}, 5*time.Minute); err != nil {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("instance %s failed to start: %v", instanceID, err))
	}

	// Get public IP
	desc, err := ec2c.DescribeInstances(awsCtx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("failed to describe instance: %v", err))
	}

	inst := desc.Reservations[0].Instances[0]
	publicIP := aws.ToString(inst.PublicIpAddress)
	privateIP := aws.ToString(inst.PrivateIpAddress)
	log.Printf("[INFO] Instance %s running at %s", instanceID, publicIP)

	// Step 7: DNS record
	if cluster.Spec.Ingress.Domain != "" && b.hostedZoneID != "" {
		_ = ctx.SendStatus(&api.ActionResult{
			UnitID:            payload.UnitID,
			SpaceID:           payload.SpaceID,
			QueuedOperationID: payload.QueuedOperationID,
			ActionResultBaseMeta: api.ActionResultMeta{
				Action:    api.ActionApply,
				Result:    api.ActionResultNone,
				Status:    api.ActionStatusProgressing,
				Message:   fmt.Sprintf("Creating DNS record for %s", cluster.Spec.Ingress.Domain),
				StartedAt: startTime,
			},
		})

		if err := b.upsertDNSRecord(awsCtx, cluster.Spec.Ingress.Domain, publicIP); err != nil {
			log.Printf("[WARN] Failed to create DNS record: %v", err)
			// Non-fatal — continue without DNS
		}
	}

	// Step 8: Poll for readiness
	_ = ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:    api.ActionApply,
			Result:    api.ActionResultNone,
			Status:    api.ActionStatusProgressing,
			Message:   "Waiting for k3s and worker to be ready",
			StartedAt: startTime,
		},
	})

	ready := b.pollInstanceReady(ctx, payload, startTime, ec2c, instanceID, 5*time.Minute)

	// Build LiveState
	ls := LiveState{
		InstanceID:      instanceID,
		PublicIP:        publicIP,
		PrivateIP:       privateIP,
		State:           "running",
		LaunchTime:      inst.LaunchTime.Format(time.RFC3339),
		SecurityGroupID: sgID,
		WorkerID:        setup.WorkerID,
		WorkerSecret:    setup.WorkerSecret,
		WorkerConnected: ready,
		K3sReady:        ready,
		DNSRecord:       cluster.Spec.Ingress.Domain,
		TargetID:        setup.TargetID,
		ConfigUnitID:    setup.ConfigUnitID,
	}
	liveStateJSON, _ := json.Marshal(ls)

	message := fmt.Sprintf("VMCluster %s is ready at %s", cluster.Metadata.Name, publicIP)
	if !ready {
		message = fmt.Sprintf("VMCluster %s launched at %s but still bootstrapping (check confighub:status tag)", cluster.Metadata.Name, publicIP)
	}

	terminatedAt := time.Now()
	return ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:       api.ActionApply,
			Result:       api.ActionResultApplyCompleted,
			Status:       api.ActionStatusCompleted,
			Message:      message,
			StartedAt:    startTime,
			TerminatedAt: &terminatedAt,
		},
		Data:      payload.Data,
		LiveData:  payload.Data,
		LiveState: liveStateJSON,
	})
}

// setupConfigHubResources creates or resolves the worker, target, and config unit.
func (b *VMClusterBridge) setupConfigHubResources(
	ctx context.Context,
	bctx api.BridgeContext,
	payload api.BridgePayload,
	startTime time.Time,
	cluster *VMCluster,
	existing *LiveState,
) (*ConfigHubSetupResult, error) {
	// Parse worker.name -> space, workerSlug
	spaceSlug, workerSlug, err := parseSlashNotation(cluster.Spec.Worker.Name)
	if err != nil {
		return nil, fmt.Errorf("invalid worker.name: %w", err)
	}

	// Parse or derive config unit slug
	var configUnitSlug string
	if cluster.Spec.Worker.Config != "" {
		_, configUnitSlug, err = parseSlashNotation(cluster.Spec.Worker.Config)
		if err != nil {
			return nil, fmt.Errorf("invalid worker.config: %w", err)
		}
	} else {
		configUnitSlug = cluster.Metadata.Name + "-worker-config"
	}

	// Derive target slug
	targetSlug := cluster.Metadata.Name + "-target"

	_ = bctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:    api.ActionApply,
			Result:    api.ActionResultNone,
			Status:    api.ActionStatusProgressing,
			Message:   fmt.Sprintf("Setting up ConfigHub resources (worker %s, target %s)", workerSlug, targetSlug),
			StartedAt: startTime,
		},
	})

	apiClient, err := NewConfigHubClient(b.confighubURL, b.confighubID, b.confighubSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate to ConfigHub API: %w", err)
	}

	// Step 1: Ensure worker exists
	creds, created, err := apiClient.EnsureWorker(ctx, spaceSlug, workerSlug)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure worker: %w", err)
	}

	workerSecret := creds.Secret
	if created {
		log.Printf("[INFO] Created worker %s (ID: %s)", workerSlug, creds.WorkerID)
	} else {
		log.Printf("[INFO] Worker %s already exists (ID: %s)", workerSlug, creds.WorkerID)
		// Recover secret from LiveState if the worker already existed
		if workerSecret == "" && existing.WorkerSecret != "" {
			workerSecret = existing.WorkerSecret
		}
		if workerSecret == "" {
			return nil, fmt.Errorf("worker %s already exists but secret is not available; delete and recreate the worker", workerSlug)
		}
	}

	// Step 2: Ensure target exists
	workerUUID, err := googleuuid.Parse(creds.WorkerID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse worker ID as UUID: %w", err)
	}

	target, err := apiClient.EnsureTarget(ctx, spaceSlug, targetSlug, workerUUID)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure target: %w", err)
	}
	log.Printf("[INFO] Target %s (ID: %s)", targetSlug, target.TargetID.String())

	// Step 3: Generate worker manifest
	manifest := generateWorkerManifest(WorkerManifestParams{
		ConfigHubURL: b.confighubURL,
		WorkerID:     creds.WorkerID,
		WorkerSecret: workerSecret,
	})

	// Step 4: Ensure config unit exists
	unit, err := apiClient.EnsureConfigUnit(ctx, spaceSlug, configUnitSlug, target.TargetID, manifest)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure config unit: %w", err)
	}
	log.Printf("[INFO] Config unit %s (ID: %s)", configUnitSlug, unit.UnitID.String())

	return &ConfigHubSetupResult{
		WorkerID:     creds.WorkerID,
		WorkerSecret: workerSecret,
		TargetID:     target.TargetID.String(),
		ConfigUnitID: unit.UnitID.String(),
		Manifest:     manifest,
	}, nil
}

// resizeInstance stops the instance, changes its type, starts it, and updates DNS.
func (b *VMClusterBridge) resizeInstance(
	ctx api.BridgeContext,
	payload api.BridgePayload,
	startTime time.Time,
	ec2c *ec2.Client,
	cluster *VMCluster,
	existing *LiveState,
	desiredType string,
) error {
	awsCtx := ctx.Context()
	instanceID := existing.InstanceID

	// Step 1: Stop the instance
	_ = ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:    api.ActionApply,
			Result:    api.ActionResultNone,
			Status:    api.ActionStatusProgressing,
			Message:   fmt.Sprintf("Stopping instance %s to change type to %s", instanceID, desiredType),
			StartedAt: startTime,
		},
	})

	_, err := ec2c.StopInstances(awsCtx, &ec2.StopInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("failed to stop instance: %v", err))
	}

	stoppedWaiter := ec2.NewInstanceStoppedWaiter(ec2c)
	if err := stoppedWaiter.Wait(awsCtx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}, 5*time.Minute); err != nil {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("instance failed to stop: %v", err))
	}
	log.Printf("[INFO] Instance %s stopped", instanceID)

	// Step 2: Change instance type
	_ = ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:    api.ActionApply,
			Result:    api.ActionResultNone,
			Status:    api.ActionStatusProgressing,
			Message:   fmt.Sprintf("Changing instance type to %s", desiredType),
			StartedAt: startTime,
		},
	})

	_, err = ec2c.ModifyInstanceAttribute(awsCtx, &ec2.ModifyInstanceAttributeInput{
		InstanceId: aws.String(instanceID),
		InstanceType: &ec2types.AttributeValue{
			Value: aws.String(desiredType),
		},
	})
	if err != nil {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("failed to change instance type: %v", err))
	}
	log.Printf("[INFO] Instance %s type changed to %s", instanceID, desiredType)

	// Step 3: Start the instance
	_ = ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:    api.ActionApply,
			Result:    api.ActionResultNone,
			Status:    api.ActionStatusProgressing,
			Message:   fmt.Sprintf("Starting instance %s", instanceID),
			StartedAt: startTime,
		},
	})

	_, err = ec2c.StartInstances(awsCtx, &ec2.StartInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("failed to start instance: %v", err))
	}

	runningWaiter := ec2.NewInstanceRunningWaiter(ec2c)
	if err := runningWaiter.Wait(awsCtx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	}, 5*time.Minute); err != nil {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("instance failed to start: %v", err))
	}

	// Get the new public IP
	desc, err := ec2c.DescribeInstances(awsCtx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return b.sendFailed(ctx, payload, startTime, fmt.Sprintf("failed to describe instance: %v", err))
	}
	inst := desc.Reservations[0].Instances[0]
	publicIP := aws.ToString(inst.PublicIpAddress)
	privateIP := aws.ToString(inst.PrivateIpAddress)
	log.Printf("[INFO] Instance %s running at %s (was %s)", instanceID, publicIP, existing.PublicIP)

	// Step 4: Update DNS if IP changed
	if publicIP != existing.PublicIP && cluster.Spec.Ingress.Domain != "" && b.hostedZoneID != "" {
		_ = ctx.SendStatus(&api.ActionResult{
			UnitID:            payload.UnitID,
			SpaceID:           payload.SpaceID,
			QueuedOperationID: payload.QueuedOperationID,
			ActionResultBaseMeta: api.ActionResultMeta{
				Action:    api.ActionApply,
				Result:    api.ActionResultNone,
				Status:    api.ActionStatusProgressing,
				Message:   fmt.Sprintf("Updating DNS for %s → %s", cluster.Spec.Ingress.Domain, publicIP),
				StartedAt: startTime,
			},
		})
		if err := b.upsertDNSRecord(awsCtx, cluster.Spec.Ingress.Domain, publicIP); err != nil {
			log.Printf("[WARN] Failed to update DNS record: %v", err)
		}
	}

	// Step 5: Wait for workers to reconnect
	_ = ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:    api.ActionApply,
			Result:    api.ActionResultNone,
			Status:    api.ActionStatusProgressing,
			Message:   "Waiting for k3s and workers to reconnect",
			StartedAt: startTime,
		},
	})

	ready := b.pollInstanceReady(ctx, payload, startTime, ec2c, instanceID, 5*time.Minute)

	// Build updated LiveState
	ls := LiveState{
		InstanceID:      instanceID,
		PublicIP:        publicIP,
		PrivateIP:       privateIP,
		State:           "running",
		LaunchTime:      existing.LaunchTime,
		SecurityGroupID: existing.SecurityGroupID,
		WorkerID:        existing.WorkerID,
		WorkerSecret:    existing.WorkerSecret,
		WorkerConnected: ready,
		K3sReady:        ready,
		DNSRecord:       existing.DNSRecord,
		TargetID:        existing.TargetID,
		ConfigUnitID:    existing.ConfigUnitID,
	}
	liveStateJSON, _ := json.Marshal(ls)

	message := fmt.Sprintf("Instance %s resized to %s at %s", instanceID, desiredType, publicIP)
	if !ready {
		message = fmt.Sprintf("Instance %s resized to %s at %s (still booting)", instanceID, desiredType, publicIP)
	}

	terminatedAt := time.Now()
	return ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:       api.ActionApply,
			Result:       api.ActionResultApplyCompleted,
			Status:       api.ActionStatusCompleted,
			Message:      message,
			StartedAt:    startTime,
			TerminatedAt: &terminatedAt,
		},
		Data:      payload.Data,
		LiveData:  payload.Data,
		LiveState: liveStateJSON,
	})
}

// getSubnetVPC resolves the VPC ID from the configured subnet.
func (b *VMClusterBridge) getSubnetVPC(ctx context.Context, ec2c *ec2.Client) (string, error) {
	if b.subnetID == "" {
		return "", fmt.Errorf("SUBNET_ID not configured")
	}
	result, err := ec2c.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		SubnetIds: []string{b.subnetID},
	})
	if err != nil {
		return "", err
	}
	if len(result.Subnets) == 0 {
		return "", fmt.Errorf("subnet %s not found", b.subnetID)
	}
	return aws.ToString(result.Subnets[0].VpcId), nil
}

// ensureSecurityGroup creates or finds the security group for a vmcluster.
func (b *VMClusterBridge) ensureSecurityGroup(ctx context.Context, ec2c *ec2.Client, clusterName, vpcID string) (string, error) {
	sgName := fmt.Sprintf("vmcluster-%s", clusterName)

	// Check if it already exists in the correct VPC
	existing, err := ec2c.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("group-name"), Values: []string{sgName}},
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
		},
	})
	if err == nil && len(existing.SecurityGroups) > 0 {
		return aws.ToString(existing.SecurityGroups[0].GroupId), nil
	}

	// Create new
	createResult, err := ec2c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(sgName),
		VpcId:       aws.String(vpcID),
		Description: aws.String(fmt.Sprintf("Security group for VMCluster %s", clusterName)),
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeSecurityGroup,
				Tags: []ec2types.Tag{
					{Key: aws.String("confighub:managed-by"), Value: aws.String("cub-vmcluster")},
					{Key: aws.String("confighub:cluster-name"), Value: aws.String(clusterName)},
				},
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create security group: %w", err)
	}

	sgID := aws.ToString(createResult.GroupId)

	// Add ingress rules: 6443 (k8s API), 80 (HTTP), 443 (HTTPS)
	ports := []int32{6443, 80, 443}
	for _, port := range ports {
		_, err := ec2c.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
			GroupId: aws.String(sgID),
			IpPermissions: []ec2types.IpPermission{
				{
					IpProtocol: aws.String("tcp"),
					FromPort:   aws.Int32(port),
					ToPort:     aws.Int32(port),
					IpRanges: []ec2types.IpRange{
						{CidrIp: aws.String("0.0.0.0/0"), Description: aws.String(fmt.Sprintf("VMCluster port %d", port))},
					},
				},
			},
		})
		if err != nil {
			log.Printf("[WARN] Failed to add ingress rule for port %d: %v", port, err)
		}
	}

	return sgID, nil
}

// resolveArch queries EC2 for the supported architectures of the given instance type.
func resolveArch(ctx context.Context, ec2c *ec2.Client, instanceType string) (arch string, amiArch string, err error) {
	result, err := ec2c.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{
		InstanceTypes: []ec2types.InstanceType{ec2types.InstanceType(instanceType)},
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to describe instance type %s: %w", instanceType, err)
	}
	if len(result.InstanceTypes) == 0 {
		return "", "", fmt.Errorf("instance type %s not found", instanceType)
	}

	for _, a := range result.InstanceTypes[0].ProcessorInfo.SupportedArchitectures {
		if a == ec2types.ArchitectureTypeArm64 {
			return "arm64", "arm64", nil
		}
	}
	return "x86_64", "amd64", nil
}

// findUbuntuAMI finds the latest Ubuntu 24.04 LTS AMI for the given instance type's architecture.
func (b *VMClusterBridge) findUbuntuAMI(ctx context.Context, ec2c *ec2.Client, instanceType string) (string, error) {
	arch, amiArch, err := resolveArch(ctx, ec2c, instanceType)
	if err != nil {
		return "", err
	}
	namePattern := fmt.Sprintf("ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-%s-server-*", amiArch)

	result, err := ec2c.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{"099720109477"}, // Canonical
		Filters: []ec2types.Filter{
			{Name: aws.String("name"), Values: []string{namePattern}},
			{Name: aws.String("state"), Values: []string{"available"}},
			{Name: aws.String("architecture"), Values: []string{arch}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to describe images: %w", err)
	}
	if len(result.Images) == 0 {
		return "", fmt.Errorf("no Ubuntu 24.04 AMI found for %s", arch)
	}

	// Find the most recent by creation date
	latest := result.Images[0]
	for _, img := range result.Images[1:] {
		if aws.ToString(img.CreationDate) > aws.ToString(latest.CreationDate) {
			latest = img
		}
	}

	return aws.ToString(latest.ImageId), nil
}

// pollInstanceReady polls the confighub:status tag until it reads "ready" or timeout.
// It reports each status transition back to ConfigHub via ctx.SendStatus.
func (b *VMClusterBridge) pollInstanceReady(bctx api.BridgeContext, payload api.BridgePayload, startTime time.Time, ec2c *ec2.Client, instanceID string, timeout time.Duration) bool {
	ctx := bctx.Context()
	deadline := time.Now().Add(timeout)
	lastStatus := ""
	for time.Now().Before(deadline) {
		desc, err := ec2c.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
			InstanceIds: []string{instanceID},
		})
		if err == nil && len(desc.Reservations) > 0 && len(desc.Reservations[0].Instances) > 0 {
			for _, tag := range desc.Reservations[0].Instances[0].Tags {
				if aws.ToString(tag.Key) == "confighub:status" {
					status := aws.ToString(tag.Value)
					if status == "ready" {
						return true
					}
					if status != lastStatus {
						lastStatus = status
						log.Printf("[INFO] Instance %s status: %s", instanceID, status)
						_ = bctx.SendStatus(&api.ActionResult{
							UnitID:            payload.UnitID,
							SpaceID:           payload.SpaceID,
							QueuedOperationID: payload.QueuedOperationID,
							ActionResultBaseMeta: api.ActionResultMeta{
								Action:    api.ActionApply,
								Result:    api.ActionResultNone,
								Status:    api.ActionStatusProgressing,
								Message:   fmt.Sprintf("Instance %s: %s", instanceID, status),
								StartedAt: startTime,
							},
						})
					}
				}
			}
		}
		time.Sleep(10 * time.Second)
	}
	return false
}

// upsertDNSRecord creates or updates A records for the cluster domain and wildcard.
func (b *VMClusterBridge) upsertDNSRecord(ctx context.Context, domain, ip string) error {
	r53c, err := b.route53Client(ctx)
	if err != nil {
		return err
	}

	// Create both the domain and wildcard records
	changes := []r53types.Change{
		{
			Action: r53types.ChangeActionUpsert,
			ResourceRecordSet: &r53types.ResourceRecordSet{
				Name: aws.String(domain),
				Type: r53types.RRTypeA,
				TTL:  aws.Int64(60),
				ResourceRecords: []r53types.ResourceRecord{
					{Value: aws.String(ip)},
				},
			},
		},
		{
			Action: r53types.ChangeActionUpsert,
			ResourceRecordSet: &r53types.ResourceRecordSet{
				Name: aws.String(fmt.Sprintf("*.%s", domain)),
				Type: r53types.RRTypeA,
				TTL:  aws.Int64(60),
				ResourceRecords: []r53types.ResourceRecord{
					{Value: aws.String(ip)},
				},
			},
		},
	}

	_, err = r53c.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(b.hostedZoneID),
		ChangeBatch: &r53types.ChangeBatch{
			Changes: changes,
			Comment: aws.String(fmt.Sprintf("VMCluster %s", domain)),
		},
	})
	return err
}

// sendFailed is a helper to report a failed apply.
func (b *VMClusterBridge) sendFailed(ctx api.BridgeContext, payload api.BridgePayload, startTime time.Time, message string) error {
	log.Printf("[ERROR] %s", message)
	terminatedAt := time.Now()
	return ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:       api.ActionApply,
			Result:       api.ActionResultApplyFailed,
			Status:       api.ActionStatusFailed,
			Message:      message,
			StartedAt:    startTime,
			TerminatedAt: &terminatedAt,
		},
	})
}
