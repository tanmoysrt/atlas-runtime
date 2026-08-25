package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// NodeConfig holds host-level identity and networking.
// It is read from /var/lib/atlas/config.json, which is separate from
// per-VM config.toml.
type NodeConfig struct {
	NodeID         int    `json:"node_id"`
	BeaconEndpoint string `json:"beacon_endpoint"`
	Network        struct {
		GreAddress string `json:"gre_address"`
	} `json:"network"`
}

// LoadNodeConfig reads node identity from a JSON file.
// It returns an error if the file is missing or node_id is not valid.
func LoadNodeConfig(path string) (*NodeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read node config: %w", err)
	}
	var config NodeConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse node config: %w", err)
	}
	if config.NodeID <= 0 {
		return nil, fmt.Errorf("node_id must be > 0")
	}
	return &config, nil
}
