package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/confighub/sdk/core/worker/api"
	"gopkg.in/yaml.v3"
)

func (b *VMClusterBridge) Destroy(ctx api.BridgeContext, payload api.BridgePayload) error {
	startTime := time.Now()

	if err := ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:    api.ActionDestroy,
			Result:    api.ActionResultNone,
			Status:    api.ActionStatusProgressing,
			Message:   "Destroying VMCluster",
			StartedAt: startTime,
		},
	}); err != nil {
		return err
	}

	// Parse cluster spec for region
	var cluster VMCluster
	if err := yaml.Unmarshal(payload.Data, &cluster); err != nil {
		return b.sendDestroyFailed(ctx, payload, startTime, fmt.Sprintf("failed to parse VMCluster YAML: %v", err))
	}

	// Parse LiveState for instance details
	var existing LiveState
	if len(payload.LiveState) > 0 {
		if err := json.Unmarshal(payload.LiveState, &existing); err != nil {
			return b.sendDestroyFailed(ctx, payload, startTime, fmt.Sprintf("failed to parse LiveState: %v", err))
		}
	}

	region := cluster.Spec.Region
	if region == "" {
		region = "us-east-1"
	}

	awsCtx := ctx.Context()

	// Step 1: Terminate instance
	if existing.InstanceID != "" {
		ec2c, err := b.ec2Client(awsCtx, region)
		if err != nil {
			return b.sendDestroyFailed(ctx, payload, startTime, fmt.Sprintf("failed to create EC2 client: %v", err))
		}

		log.Printf("[INFO] Terminating instance %s", existing.InstanceID)
		_, err = ec2c.TerminateInstances(awsCtx, &ec2.TerminateInstancesInput{
			InstanceIds: []string{existing.InstanceID},
		})
		if err != nil {
			log.Printf("[WARN] Failed to terminate instance %s: %v", existing.InstanceID, err)
		}

		// Wait for termination
		waiter := ec2.NewInstanceTerminatedWaiter(ec2c)
		if err := waiter.Wait(awsCtx, &ec2.DescribeInstancesInput{
			InstanceIds: []string{existing.InstanceID},
		}, 5*time.Minute); err != nil {
			log.Printf("[WARN] Timeout waiting for instance %s to terminate: %v", existing.InstanceID, err)
		}

		// Step 2: Delete security group (after instance terminated)
		if existing.SecurityGroupID != "" {
			log.Printf("[INFO] Deleting security group %s", existing.SecurityGroupID)
			_, err = ec2c.DeleteSecurityGroup(awsCtx, &ec2.DeleteSecurityGroupInput{
				GroupId: aws.String(existing.SecurityGroupID),
			})
			if err != nil {
				log.Printf("[WARN] Failed to delete security group %s: %v", existing.SecurityGroupID, err)
			}
		}
	}

	// Step 3: Remove DNS records
	if existing.DNSRecord != "" && existing.PublicIP != "" && b.hostedZoneID != "" {
		log.Printf("[INFO] Removing DNS records for %s", existing.DNSRecord)
		if err := b.deleteDNSRecord(awsCtx, existing.DNSRecord, existing.PublicIP); err != nil {
			log.Printf("[WARN] Failed to delete DNS record: %v", err)
		}
	}

	terminatedAt := time.Now()
	return ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:       api.ActionDestroy,
			Result:       api.ActionResultDestroyCompleted,
			Status:       api.ActionStatusCompleted,
			Message:      fmt.Sprintf("VMCluster %s destroyed", cluster.Metadata.Name),
			StartedAt:    startTime,
			TerminatedAt: &terminatedAt,
		},
		LiveData: []byte{},
	})
}

// deleteDNSRecord removes A records for the domain and wildcard.
func (b *VMClusterBridge) deleteDNSRecord(ctx context.Context, domain, ip string) error {
	r53c, err := b.route53Client(ctx)
	if err != nil {
		return err
	}

	changes := []r53types.Change{
		{
			Action: r53types.ChangeActionDelete,
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
			Action: r53types.ChangeActionDelete,
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
			Comment: aws.String(fmt.Sprintf("Destroy VMCluster %s", domain)),
		},
	})
	return err
}

func (b *VMClusterBridge) sendDestroyFailed(ctx api.BridgeContext, payload api.BridgePayload, startTime time.Time, message string) error {
	log.Printf("[ERROR] Destroy: %s", message)
	terminatedAt := time.Now()
	return ctx.SendStatus(&api.ActionResult{
		UnitID:            payload.UnitID,
		SpaceID:           payload.SpaceID,
		QueuedOperationID: payload.QueuedOperationID,
		ActionResultBaseMeta: api.ActionResultMeta{
			Action:       api.ActionDestroy,
			Result:       api.ActionResultDestroyFailed,
			Status:       api.ActionStatusFailed,
			Message:      message,
			StartedAt:    startTime,
			TerminatedAt: &terminatedAt,
		},
	})
}
