package main

// VMCluster represents the KRM resource for a single-node k3s cluster on EC2.
type VMCluster struct {
	APIVersion string          `yaml:"apiVersion"`
	Kind       string          `yaml:"kind"`
	Metadata   VMClusterMeta   `yaml:"metadata"`
	Spec       VMClusterSpec   `yaml:"spec"`
}

type VMClusterMeta struct {
	Name   string            `yaml:"name"`
	Labels map[string]string `yaml:"labels,omitempty"`
}

type VMClusterSpec struct {
	InstanceType            string     `yaml:"instanceType"`
	Region                  string     `yaml:"region"`
	DiskSizeGB              int        `yaml:"diskSizeGB"`
	K3sVersion              string     `yaml:"k3sVersion"`
	Ingress                 IngressSpec `yaml:"ingress"`
	Worker                  WorkerSpec `yaml:"worker"`
	InstallVMClusterWorker  bool       `yaml:"installVMClusterWorker,omitempty"`
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
	ConfigHubURL  string   `yaml:"confighubURL"`
	Slug          string   `yaml:"slug"`
	SpaceSlug     string   `yaml:"spaceSlug"`
	ProviderTypes []string `yaml:"providerTypes"`

	// Deprecated: use slug + spaceSlug instead. These are still supported
	// for backward compatibility but will be removed.
	WorkerID     string `yaml:"workerID,omitempty"`
	WorkerSecret string `yaml:"workerSecret,omitempty"`
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
}
