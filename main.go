package main

import (
	"log"
	"os"

	"github.com/confighub/sdk/core/worker"
)

func main() {
	log.Printf("[INFO] Starting cub-vmcluster worker...")

	bridge, err := NewVMClusterBridge(VMClusterBridgeConfig{
		ConfigHubURL:    os.Getenv("CONFIGHUB_URL"),
		ConfigHubID:     os.Getenv("CONFIGHUB_WORKER_ID"),
		ConfigHubSecret: os.Getenv("CONFIGHUB_WORKER_SECRET"),
	})
	if err != nil {
		log.Fatalf("Failed to create VMCluster bridge: %v", err)
	}

	bridgeDispatcher := worker.NewBridgeDispatcher()
	bridgeDispatcher.RegisterBridge(bridge)

	connector, err := worker.NewConnector(worker.ConnectorOptions{
		WorkerID:         os.Getenv("CONFIGHUB_WORKER_ID"),
		WorkerSecret:     os.Getenv("CONFIGHUB_WORKER_SECRET"),
		ConfigHubURL:     os.Getenv("CONFIGHUB_URL"),
		BridgeDispatcher: &bridgeDispatcher,
	})
	if err != nil {
		log.Fatalf("Failed to create connector: %v", err)
	}

	log.Printf("[INFO] Connecting to ConfigHub...")
	if err := connector.Start(); err != nil {
		log.Fatalf("Failed to start connector: %v", err)
	}
}
