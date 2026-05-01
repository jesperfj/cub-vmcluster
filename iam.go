package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

const (
	perVMRolePrefix     = "cub-vmcluster-"
	managedPolicySSMARN = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
	ec2TrustPolicy      = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
)

// perVMRoleName returns the IAM role / instance profile name for the given unit ID.
func perVMRoleName(unitID string) string {
	return perVMRolePrefix + unitID
}

func (b *VMClusterBridge) iamClient(ctx context.Context, roleARN string) (*iam.Client, error) {
	cfg, err := b.assumeRoleConfig(ctx, roleARN)
	if err != nil {
		return nil, err
	}
	return iam.NewFromConfig(cfg), nil
}

// ensurePerVMInstanceProfile creates (or recovers) an IAM role + instance profile
// scoped to a single VM. Returns the instance profile name.
func (b *VMClusterBridge) ensurePerVMInstanceProfile(ctx context.Context, roleARN, unitID, region string, includeVMClusterOps bool) (string, error) {
	name := perVMRoleName(unitID)
	iamc, err := b.iamClient(ctx, roleARN)
	if err != nil {
		return "", err
	}

	created, err := b.ensureRole(ctx, iamc, name)
	if err != nil {
		return "", fmt.Errorf("ensure role: %w", err)
	}

	if _, err := iamc.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
		RoleName:  aws.String(name),
		PolicyArn: aws.String(managedPolicySSMARN),
	}); err != nil && !isAlreadyAttached(err) {
		return "", fmt.Errorf("attach SSM managed policy: %w", err)
	}

	ssmPolicy := buildSSMParameterStorePolicy(unitID)
	if _, err := iamc.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(name),
		PolicyName:     aws.String("ssm-parameter-store"),
		PolicyDocument: aws.String(ssmPolicy),
	}); err != nil {
		return "", fmt.Errorf("put SSM parameter store policy: %w", err)
	}

	if includeVMClusterOps {
		account, err := b.callerAccountID(ctx, roleARN)
		if err != nil {
			return "", fmt.Errorf("resolve account ID: %w", err)
		}
		opsPolicy := buildVMClusterOpsPolicy(account, region)
		if _, err := iamc.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
			RoleName:       aws.String(name),
			PolicyName:     aws.String("vmcluster-ops"),
			PolicyDocument: aws.String(opsPolicy),
		}); err != nil {
			return "", fmt.Errorf("put vmcluster-ops policy: %w", err)
		}
	} else {
		// Idempotency: drop the policy if it was previously set and the user toggled installVMClusterWorker off.
		_, _ = iamc.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
			RoleName: aws.String(name), PolicyName: aws.String("vmcluster-ops"),
		})
	}

	if err := b.ensureInstanceProfile(ctx, iamc, name); err != nil {
		return "", fmt.Errorf("ensure instance profile: %w", err)
	}

	if created {
		// New role/profile — let IAM propagate before EC2 RunInstances picks it up.
		log.Printf("[INFO] Waiting 10s for new IAM instance profile %s to propagate", name)
		time.Sleep(10 * time.Second)
	}

	return name, nil
}

// deletePerVMInstanceProfile tears down the role + instance profile for a unit.
// Safe to call when the resources don't exist (e.g. legacy VMs).
func (b *VMClusterBridge) deletePerVMInstanceProfile(ctx context.Context, roleARN, unitID string) error {
	name := perVMRoleName(unitID)
	iamc, err := b.iamClient(ctx, roleARN)
	if err != nil {
		return err
	}

	if _, err := iamc.RemoveRoleFromInstanceProfile(ctx, &iam.RemoveRoleFromInstanceProfileInput{
		InstanceProfileName: aws.String(name),
		RoleName:            aws.String(name),
	}); err != nil && !isNoSuchEntity(err) {
		log.Printf("[WARN] RemoveRoleFromInstanceProfile %s: %v", name, err)
	}
	if _, err := iamc.DeleteInstanceProfile(ctx, &iam.DeleteInstanceProfileInput{
		InstanceProfileName: aws.String(name),
	}); err != nil && !isNoSuchEntity(err) {
		log.Printf("[WARN] DeleteInstanceProfile %s: %v", name, err)
	}

	for _, p := range []string{"ssm-parameter-store", "vmcluster-ops"} {
		if _, err := iamc.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{
			RoleName: aws.String(name), PolicyName: aws.String(p),
		}); err != nil && !isNoSuchEntity(err) {
			log.Printf("[WARN] DeleteRolePolicy %s/%s: %v", name, p, err)
		}
	}
	if _, err := iamc.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{
		RoleName: aws.String(name), PolicyArn: aws.String(managedPolicySSMARN),
	}); err != nil && !isNoSuchEntity(err) {
		log.Printf("[WARN] DetachRolePolicy %s: %v", name, err)
	}
	if _, err := iamc.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)}); err != nil && !isNoSuchEntity(err) {
		log.Printf("[WARN] DeleteRole %s: %v", name, err)
	}
	return nil
}

func (b *VMClusterBridge) ensureRole(ctx context.Context, iamc *iam.Client, name string) (created bool, err error) {
	if _, gerr := iamc.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)}); gerr == nil {
		return false, nil
	}
	_, err = iamc.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(name),
		AssumeRolePolicyDocument: aws.String(ec2TrustPolicy),
		Description:              aws.String("Per-VM role for cub-vmcluster instance"),
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (b *VMClusterBridge) ensureInstanceProfile(ctx context.Context, iamc *iam.Client, name string) error {
	gp, gerr := iamc.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{InstanceProfileName: aws.String(name)})
	if gerr != nil {
		if !isNoSuchEntity(gerr) {
			return gerr
		}
		if _, err := iamc.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{
			InstanceProfileName: aws.String(name),
		}); err != nil {
			return err
		}
		gp = nil
	}
	hasRole := false
	if gp != nil && gp.InstanceProfile != nil {
		for _, r := range gp.InstanceProfile.Roles {
			if aws.ToString(r.RoleName) == name {
				hasRole = true
				break
			}
		}
	}
	if !hasRole {
		if _, err := iamc.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{
			InstanceProfileName: aws.String(name),
			RoleName:            aws.String(name),
		}); err != nil && !isAlreadyAttached(err) {
			return err
		}
	}
	return nil
}

func (b *VMClusterBridge) callerAccountID(ctx context.Context, roleARN string) (string, error) {
	cfg, err := b.assumeRoleConfig(ctx, roleARN)
	if err != nil {
		return "", err
	}
	resp, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", err
	}
	return aws.ToString(resp.Account), nil
}

func buildSSMParameterStorePolicy(unitID string) string {
	return fmt.Sprintf(`{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ParameterStoreReadWrite",
      "Effect": "Allow",
      "Action": [
        "ssm:GetParameter",
        "ssm:GetParameters",
        "ssm:GetParametersByPath",
        "ssm:PutParameter",
        "ssm:DeleteParameter",
        "ssm:DeleteParameters",
        "ssm:DescribeParameters",
        "ssm:LabelParameterVersion"
      ],
      "Resource": "arn:aws:ssm:*:*:parameter/cub-vmcluster/%s/*"
    },
    {
      "Sid": "KMSDecryptForSecureString",
      "Effect": "Allow",
      "Action": ["kms:Decrypt", "kms:Encrypt", "kms:GenerateDataKey"],
      "Resource": "*",
      "Condition": {"StringLike": {"kms:EncryptionContext:PARAMETER_ARN": "arn:aws:ssm:*:*:parameter/cub-vmcluster/%s/*"}}
    },
    {
      "Sid": "TagOwnInstance",
      "Effect": "Allow",
      "Action": "ec2:CreateTags",
      "Resource": "arn:aws:ec2:*:*:instance/*",
      "Condition": {"StringEquals": {"ec2:ResourceTag/confighub:unit-id": "%s"}}
    }
  ]
}`, unitID, unitID, unitID)
}

func buildVMClusterOpsPolicy(account, region string) string {
	return fmt.Sprintf(`{
  "Version": "2012-10-17",
  "Statement": [
    {"Sid": "EC2", "Effect": "Allow", "Action": "ec2:*", "Resource": "*",
     "Condition": {"StringEquals": {"aws:RequestedRegion": ["%s"]}}},
    {"Sid": "Route53", "Effect": "Allow",
     "Action": ["route53:ChangeResourceRecordSets", "route53:GetHostedZone", "route53:ListResourceRecordSets"],
     "Resource": "arn:aws:route53:::hostedzone/*"},
    {"Sid": "IAM", "Effect": "Allow",
     "Action": ["iam:CreateRole","iam:DeleteRole","iam:GetRole","iam:PutRolePolicy","iam:DeleteRolePolicy","iam:GetRolePolicy","iam:AttachRolePolicy","iam:DetachRolePolicy","iam:CreateInstanceProfile","iam:DeleteInstanceProfile","iam:GetInstanceProfile","iam:AddRoleToInstanceProfile","iam:RemoveRoleFromInstanceProfile"],
     "Resource": ["arn:aws:iam::%s:role/cub-vmcluster-*","arn:aws:iam::%s:instance-profile/cub-vmcluster-*"]},
    {"Sid": "IAMPassRole", "Effect": "Allow", "Action": "iam:PassRole",
     "Resource": "arn:aws:iam::%s:role/cub-vmcluster-*"},
    {"Sid": "Tagging", "Effect": "Allow", "Action": "ec2:CreateTags",
     "Resource": "arn:aws:ec2:*:*:instance/*"},
    {"Sid": "SSMCommands", "Effect": "Allow",
     "Action": ["ssm:SendCommand", "ssm:GetCommandInvocation"], "Resource": "*"}
  ]
}`, region, account, account, account)
}

func isNoSuchEntity(err error) bool {
	var nse *iamtypes.NoSuchEntityException
	return errors.As(err, &nse)
}

func isAlreadyAttached(err error) bool {
	// CreateRole/AttachRolePolicy/AddRoleToInstanceProfile may surface "already exists"-style errors.
	var ee *iamtypes.EntityAlreadyExistsException
	if errors.As(err, &ee) {
		return true
	}
	var le *iamtypes.LimitExceededException
	return errors.As(err, &le) // belt-and-suspenders for double-attach edge cases
}
