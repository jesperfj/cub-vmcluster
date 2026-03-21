package main

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/confighub/sdk/core/worker/api"
	"gopkg.in/yaml.v3"
)

func (b *VMClusterBridge) Refresh(ctx api.BridgeContext, payload api.BridgePayload) error {
	startTime := time.Now()

	if err := ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:    api.ActionRefresh,
			Result:    api.ActionResultNone,
			Status:    api.ActionStatusProgressing,
			Message:   "Refreshing VMCluster state",
			StartedAt: startTime,
		},
	}); err != nil {
		return err
	}

	// Parse cluster spec for region
	var cluster VMCluster
	if err := yaml.Unmarshal(payload.Data, &cluster); err != nil {
		return b.sendRefreshFailed(ctx, payload, startTime, fmt.Sprintf("failed to parse VMCluster YAML: %v", err))
	}

	// Parse existing LiveState
	var existing LiveState
	if len(payload.LiveState) > 0 {
		if err := json.Unmarshal(payload.LiveState, &existing); err != nil {
			return b.sendRefreshFailed(ctx, payload, startTime, fmt.Sprintf("failed to parse LiveState: %v", err))
		}
	}

	if existing.InstanceID == "" {
		// No instance — drift (expected state exists but actual doesn't)
		terminatedAt := time.Now()
		return ctx.SendStatus(&api.ActionResult{
			UnitID:            payload.UnitID,
			SpaceID:           payload.SpaceID,
			QueuedOperationID: payload.QueuedOperationID,
			ActionResultBaseMeta: api.ActionResultMeta{
				Action:       api.ActionRefresh,
				Result:       api.ActionResultRefreshAndDrifted,
				Status:       api.ActionStatusCompleted,
				Message:      "No instance found in LiveState",
				StartedAt:    startTime,
				TerminatedAt: &terminatedAt,
			},
		})
	}

	region := cluster.Spec.Region
	if region == "" {
		region = "us-east-1"
	}

	ec2c, err := b.ec2Client(ctx.Context(), region)
	if err != nil {
		return b.sendRefreshFailed(ctx, payload, startTime, fmt.Sprintf("failed to create EC2 client: %v", err))
	}

	desc, err := ec2c.DescribeInstances(ctx.Context(), &ec2.DescribeInstancesInput{
		InstanceIds: []string{existing.InstanceID},
	})
	if err != nil {
		return b.sendRefreshFailed(ctx, payload, startTime, fmt.Sprintf("failed to describe instance %s: %v", existing.InstanceID, err))
	}

	if len(desc.Reservations) == 0 || len(desc.Reservations[0].Instances) == 0 {
		terminatedAt := time.Now()
		return ctx.SendStatus(&api.ActionResult{
			UnitID:            payload.UnitID,
			SpaceID:           payload.SpaceID,
			QueuedOperationID: payload.QueuedOperationID,
			ActionResultBaseMeta: api.ActionResultMeta{
				Action:       api.ActionRefresh,
				Result:       api.ActionResultRefreshAndDrifted,
				Status:       api.ActionStatusCompleted,
				Message:      fmt.Sprintf("Instance %s not found", existing.InstanceID),
				StartedAt:    startTime,
				TerminatedAt: &terminatedAt,
			},
		})
	}

	inst := desc.Reservations[0].Instances[0]
	stateName := string(inst.State.Name)

	// Check confighub:status tag
	var confighubStatus string
	for _, tag := range inst.Tags {
		if aws.ToString(tag.Key) == "confighub:status" {
			confighubStatus = aws.ToString(tag.Value)
		}
	}

	// Update LiveState
	updated := LiveState{
		InstanceID:      existing.InstanceID,
		PublicIP:        aws.ToString(inst.PublicIpAddress),
		PrivateIP:       aws.ToString(inst.PrivateIpAddress),
		State:           stateName,
		LaunchTime:      existing.LaunchTime,
		SecurityGroupID: existing.SecurityGroupID,
		WorkerID:        existing.WorkerID,
		WorkerConnected: confighubStatus == "ready",
		K3sReady:        confighubStatus == "ready",
		DNSRecord:       existing.DNSRecord,
		Kubeconfig:      existing.Kubeconfig,
	}
	if inst.LaunchTime != nil {
		updated.LaunchTime = inst.LaunchTime.Format(time.RFC3339)
	}

	updatedJSON, _ := json.Marshal(updated)

	// Determine drift
	drifted := inst.State.Name == ec2types.InstanceStateNameTerminated ||
		inst.State.Name == ec2types.InstanceStateNameStopped ||
		inst.State.Name == ec2types.InstanceStateNameShuttingDown

	resultType := api.ActionResultRefreshAndNoDrift
	message := fmt.Sprintf("Instance %s is %s", existing.InstanceID, stateName)
	if drifted {
		resultType = api.ActionResultRefreshAndDrifted
		message = fmt.Sprintf("Instance %s has drifted (state: %s)", existing.InstanceID, stateName)
	}

	terminatedAt := time.Now()
	return ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:       api.ActionRefresh,
			Result:       resultType,
			Status:       api.ActionStatusCompleted,
			Message:      message,
			StartedAt:    startTime,
			TerminatedAt: &terminatedAt,
		},
		Data:      payload.Data,
		LiveData:  payload.Data,
		LiveState: updatedJSON,
	})
}

func (b *VMClusterBridge) sendRefreshFailed(ctx api.BridgeContext, payload api.BridgePayload, startTime time.Time, message string) error {
	log.Printf("[ERROR] Refresh: %s", message)
	terminatedAt := time.Now()
	return ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:       api.ActionRefresh,
			Result:       api.ActionResultRefreshFailed,
			Status:       api.ActionStatusFailed,
			Message:      message,
			StartedAt:    startTime,
			TerminatedAt: &terminatedAt,
		},
	})
}
