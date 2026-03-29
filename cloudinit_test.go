package main

import (
	"strings"
	"testing"
)

func TestRenderUserDataIngressEnabled(t *testing.T) {
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

	mustContain := []struct {
		name string
		want string
	}{
		{"shebang", "#!/bin/bash"},
		{"k3s version", "v1.35.2+k3s1"},
		{"tls-san domain", "--tls-san test.demo.example.com"},
		{"disable metrics-server", "--disable metrics-server"},
		{"disable servicelb", "--disable servicelb"},
		{"worker image", "ghcr.io/confighubai/confighub-worker:latest"},
		{"confighub url", "https://app.confighub.com"},
		{"worker id", "wkr_test"},
		{"worker secret", "ch_testsecret"},
		{"provider types", "Kubernetes"},
		{"cert-manager", "cert-manager.yaml"},
		{"letsencrypt email", "ops@example.com"},
		{"ready tag", `tag_status "ready"`},
	}
	for _, c := range mustContain {
		if !strings.Contains(userData, c.want) {
			t.Errorf("%s: expected %q in user-data, not found", c.name, c.want)
		}
	}

	mustNotContain := []struct {
		name string
		bad  string
	}{
		{"traefik not disabled", "--disable traefik"},
	}
	for _, c := range mustNotContain {
		if strings.Contains(userData, c.bad) {
			t.Errorf("%s: did not expect %q in user-data", c.name, c.bad)
		}
	}
}

func TestRenderUserDataNoIngress(t *testing.T) {
	cluster := &VMCluster{
		APIVersion: "demo.confighub.com/v1alpha1",
		Kind:       "VMCluster",
		Metadata:   VMClusterMeta{Name: "no-ingress"},
		Spec: VMClusterSpec{
			K3sVersion: "v1.35.2+k3s1",
			Ingress: IngressSpec{
				Enabled: false,
			},
			Worker: WorkerSpec{
				ConfigHubURL:  "https://app.confighub.com",
				WorkerID:      "wkr_noing",
				WorkerSecret:  "ch_secret",
				ProviderTypes: []string{"Kubernetes"},
			},
		},
	}

	userData, err := renderUserData(cluster, cluster.Spec.Worker.WorkerID, cluster.Spec.Worker.WorkerSecret, &VMClusterBridge{})
	if err != nil {
		t.Fatalf("renderUserData failed: %v", err)
	}

	mustContain := []struct {
		name string
		want string
	}{
		{"disable traefik", "--disable traefik"},
		{"disable metrics-server", "--disable metrics-server"},
		{"disable servicelb", "--disable servicelb"},
	}
	for _, c := range mustContain {
		if !strings.Contains(userData, c.want) {
			t.Errorf("%s: expected %q in user-data, not found", c.name, c.want)
		}
	}

	mustNotContain := []struct {
		name string
		bad  string
	}{
		{"no cert-manager", "cert-manager"},
	}
	for _, c := range mustNotContain {
		if strings.Contains(userData, c.bad) {
			t.Errorf("%s: did not expect %q in user-data", c.name, c.bad)
		}
	}
}
