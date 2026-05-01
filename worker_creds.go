package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// ssmWorkerIDPath returns the SSM parameter path for the cub-worker ID for a given unit.
func ssmWorkerIDPath(unitID string) string {
	return fmt.Sprintf("/cub-vmcluster/%s/cub-worker-id", unitID)
}

// ssmWorkerSecretPath returns the SSM parameter path for the cub-worker Secret for a given unit.
func ssmWorkerSecretPath(unitID string) string {
	return fmt.Sprintf("/cub-vmcluster/%s/cub-worker-secret", unitID)
}

// writeWorkerCredsToSSM stores the cub-worker ID/Secret in SSM Parameter Store so the VM
// can fetch them at boot. The Secret is stored as a SecureString.
func (b *VMClusterBridge) writeWorkerCredsToSSM(ctx context.Context, roleARN, region, unitID, workerID, workerSecret string) error {
	ssmc, err := b.ssmClient(ctx, roleARN, region)
	if err != nil {
		return fmt.Errorf("ssm client: %w", err)
	}
	overwrite := true
	if _, err := ssmc.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(ssmWorkerIDPath(unitID)),
		Value:     aws.String(workerID),
		Type:      ssmtypes.ParameterTypeString,
		Overwrite: &overwrite,
	}); err != nil {
		return fmt.Errorf("put worker-id: %w", err)
	}
	if _, err := ssmc.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(ssmWorkerSecretPath(unitID)),
		Value:     aws.String(workerSecret),
		Type:      ssmtypes.ParameterTypeSecureString,
		Overwrite: &overwrite,
	}); err != nil {
		return fmt.Errorf("put worker-secret: %w", err)
	}
	return nil
}

// deleteWorkerCredsFromSSM removes the SSM parameters created by writeWorkerCredsToSSM.
// Best-effort: missing parameters are not an error.
func (b *VMClusterBridge) deleteWorkerCredsFromSSM(ctx context.Context, roleARN, region, unitID string) {
	ssmc, err := b.ssmClient(ctx, roleARN, region)
	if err != nil {
		log.Printf("[WARN] ssm client for cred cleanup: %v", err)
		return
	}
	for _, name := range []string{ssmWorkerIDPath(unitID), ssmWorkerSecretPath(unitID)} {
		if _, err := ssmc.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(name)}); err != nil {
			var nfe *ssmtypes.ParameterNotFound
			if errors.As(err, &nfe) {
				continue
			}
			log.Printf("[WARN] delete SSM parameter %s: %v", name, err)
		}
	}
}
