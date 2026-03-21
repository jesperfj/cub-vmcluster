package main

import (
	"strings"
	"testing"
)

func TestRenderUserData(t *testing.T) {
	cluster := &VMCluster{
		APIVersion: "demo.confighub.com/v1alpha1",
		Kind:       "VMCluster",
		Metadata: VMClusterMeta{
			Name: "test-cluster",
		},
		Spec: VMClusterSpec{
			InstanceType: "t3.medium",
			Region:       "us-east-1",
			DiskSizeGB:   30,
			K3sVersion:   "v1.35.2+k3s1",
			Ingress: IngressSpec{
				Enabled: true,
				Domain:  "test.demo.example.com",
				TLS: TLSSpec{
					Enabled: true,
					Email:   "ops@example.com",
				},
			},
			Worker: WorkerSpec{
				ConfigHubURL:  "https://app.confighub.com",
				WorkerID:      "wkr_test",
				WorkerSecret:  "ch_testsecret",
				ProviderTypes: []string{"Kubernetes"},
			},
		},
	}

	userData, err := renderUserData(cluster, cluster.Spec.Worker.WorkerID, cluster.Spec.Worker.WorkerSecret, &VMClusterBridge{})
	if err != nil {
		t.Fatalf("renderUserData failed: %v", err)
	}

	// Verify key content is present
	checks := []struct {
		name string
		want string
	}{
		{"shebang", "#!/bin/bash"},
		{"k3s version", "v1.35.2+k3s1"},
		{"tls-san domain", "--tls-san test.demo.example.com"},
		{"worker image", "ghcr.io/confighubai/confighub-worker:latest"},
		{"confighub url", "https://app.confighub.com"},
		{"worker id", "wkr_test"},
		{"worker secret", "ch_testsecret"},
		{"provider types", "Kubernetes"},
		{"cert-manager", "cert-manager.yaml"},
		{"letsencrypt email", "ops@example.com"},
		{"ready tag", `tag_status "ready"`},
	}

	for _, c := range checks {
		if !strings.Contains(userData, c.want) {
			t.Errorf("%s: expected %q in user-data, not found", c.name, c.want)
		}
	}
}

func TestRenderUserDataNoTLS(t *testing.T) {
	cluster := &VMCluster{
		APIVersion: "demo.confighub.com/v1alpha1",
		Kind:       "VMCluster",
		Metadata:   VMClusterMeta{Name: "no-tls"},
		Spec: VMClusterSpec{
			K3sVersion: "v1.30.0+k3s1",
			Ingress: IngressSpec{
				Domain: "notls.example.com",
				TLS:    TLSSpec{Enabled: false},
			},
			Worker: WorkerSpec{
				ConfigHubURL:  "https://app.confighub.com",
				WorkerID:      "wkr_notls",
				WorkerSecret:  "ch_secret",
				ProviderTypes: []string{"Kubernetes"},
			},
		},
	}

	userData, err := renderUserData(cluster, cluster.Spec.Worker.WorkerID, cluster.Spec.Worker.WorkerSecret, &VMClusterBridge{})
	if err != nil {
		t.Fatalf("renderUserData failed: %v", err)
	}

	if strings.Contains(userData, "cert-manager") {
		t.Error("expected no cert-manager when TLS disabled")
	}
}
