package main

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed cloudinit/userdata.sh.tmpl
var cloudinitFS embed.FS

type CloudInitParams struct {
	ClusterName   string
	K3sVersion    string
	ConfigHubURL  string
	WorkerID      string
	WorkerSecret  string
	ProviderTypes string
	IngressDomain string
	TLSEnabled    bool
	TLSEmail      string
	WorkerImage   string

	// VMCluster worker self-install
	InstallVMClusterWorker   bool
	VMClusterWorkerID        string
	VMClusterWorkerSecret    string
	VMClusterWorkerImage     string
	VMClusterSubnetID        string
	VMClusterHostedZoneID    string
	VMClusterInstanceProfile string
	AWSRegion                string
}

func renderUserData(cluster *VMCluster, workerID, workerSecret string, bridge *VMClusterBridge) (string, error) {
	tmplData, err := cloudinitFS.ReadFile("cloudinit/userdata.sh.tmpl")
	if err != nil {
		return "", fmt.Errorf("failed to read userdata template: %w", err)
	}

	tmpl, err := template.New("userdata").Parse(string(tmplData))
	if err != nil {
		return "", fmt.Errorf("failed to parse userdata template: %w", err)
	}

	workerImage := cluster.Spec.Worker.Image
	if workerImage == "" {
		workerImage = "ghcr.io/confighubai/confighub-worker:latest"
	}

	vmclusterImage := cluster.Spec.VMClusterWorkerImage
	if vmclusterImage == "" {
		vmclusterImage = "ghcr.io/jesperfj/cub-vmcluster:latest"
	}

	params := CloudInitParams{
		ClusterName:   cluster.Metadata.Name,
		K3sVersion:    cluster.Spec.K3sVersion,
		ConfigHubURL:  cluster.Spec.Worker.ConfigHubURL,
		WorkerID:      workerID,
		WorkerSecret:  workerSecret,
		ProviderTypes: strings.Join(cluster.Spec.Worker.ProviderTypes, ","),
		IngressDomain: cluster.Spec.Ingress.Domain,
		TLSEnabled:    cluster.Spec.Ingress.TLS.Enabled,
		TLSEmail:      cluster.Spec.Ingress.TLS.Email,
		WorkerImage:   workerImage,

		InstallVMClusterWorker:   cluster.Spec.InstallVMClusterWorker,
		VMClusterWorkerID:        bridge.confighubID,
		VMClusterWorkerSecret:    bridge.confighubSecret,
		VMClusterWorkerImage:     vmclusterImage,
		VMClusterSubnetID:        bridge.subnetID,
		VMClusterHostedZoneID:    bridge.hostedZoneID,
		VMClusterInstanceProfile: bridge.instanceProfileID,
		AWSRegion:                cluster.Spec.Region,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, params); err != nil {
		return "", fmt.Errorf("failed to render userdata template: %w", err)
	}

	return buf.String(), nil
}
