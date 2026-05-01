package main

import (
	"fmt"
	"strings"
)

// VMCluster represents the KRM resource for a single-node k3s cluster on EC2.
type VMCluster struct {
	APIVersion string        `yaml:"apiVersion"`
	Kind       string        `yaml:"kind"`
	Metadata   VMClusterMeta `yaml:"metadata"`
	Spec       VMClusterSpec `yaml:"spec"`
}

type VMClusterMeta struct {
	Name   string            `yaml:"name"`
	Labels map[string]string `yaml:"labels,omitempty"`
}

type VMClusterSpec struct {
	InstanceType           string      `yaml:"instanceType"`
	DiskSizeGB             int         `yaml:"diskSizeGB"`
	K3sVersion             string      `yaml:"k3sVersion"`
	Ingress                IngressSpec `yaml:"ingress"`
	Worker                 WorkerSpec  `yaml:"worker"`
	InstallVMClusterWorker bool        `yaml:"installVMClusterWorker,omitempty"`
	VMClusterWorkerImage   string      `yaml:"vmclusterWorkerImage,omitempty"`
}

// BridgeTargetOptions holds the per-target configuration the controller pulls
// out of payload.TargetOptions on each Apply/Refresh/Destroy.
type BridgeTargetOptions struct {
	SubnetID     string
	HostedZoneID string
	Region       string
	RoleARN      string
}

func parseTargetOptions(opts map[string]string) BridgeTargetOptions {
	return BridgeTargetOptions{
		SubnetID:     opts["SubnetID"],
		HostedZoneID: opts["HostedZoneID"],
		Region:       opts["Region"],
		RoleARN:      opts["RoleARN"],
	}
}

type IngressSpec struct {
	Enabled bool    `yaml:"enabled"`
	Domain  string  `yaml:"domain"`
	TLS     TLSSpec `yaml:"tls"`
}

type TLSSpec struct {
	Enabled bool   `yaml:"enabled"`
	Email   string `yaml:"email"`
}

type WorkerSpec struct {
	Name                 string `yaml:"name"`                           // "space/worker-name" slash notation
	Config               string `yaml:"config,omitempty"`               // "space/unit-name", defaults to {space}/{metadata.name}-worker-config
	TenantOrganizationID string `yaml:"tenantOrganizationID,omitempty"` // External org ID (e.g. "org_xxx") set when the cluster has been reassigned to a tenant org
}

// LiveState is the JSON structure stored in the unit's LiveState field.
type LiveState struct {
	InstanceID      string `json:"instanceID"`
	PublicIP        string `json:"publicIP"`
	PrivateIP       string `json:"privateIP"`
	State           string `json:"state"`
	LaunchTime      string `json:"launchTime"`
	SecurityGroupID string `json:"securityGroupID"`
	WorkerID        string `json:"workerID"`
	WorkerConnected bool   `json:"workerConnected"`
	K3sReady        bool   `json:"k3sReady"`
	DNSRecord       string `json:"dnsRecord"`
	Kubeconfig      string `json:"kubeconfig,omitempty"`
	TargetID        string `json:"targetID,omitempty"`
	ConfigUnitID    string `json:"configUnitID,omitempty"`
}

// parseSlashNotation parses a "space/name" reference into its components.
func parseSlashNotation(s string) (space, name string, err error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected 'space/name' format, got %q", s)
	}
	return parts[0], parts[1], nil
}
