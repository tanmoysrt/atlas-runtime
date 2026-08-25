package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Metadata is the persistent state for a single VM.
type Metadata struct {
	InstanceID   string `json:"instance_id"`
	Hostname     string `json:"hostname"`
	Initialized  bool   `json:"initialized"`
	DesiredState string `json:"desired_state"`
	PrivateIP    string `json:"private_ip"`
	PublicIPv4   string `json:"public_ipv4"`
	PublicIPv6   string `json:"public_ipv6"`
}

// LoadMetadata reads a metadata.json file into a Metadata struct.
func LoadMetadata(path string) (*Metadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("metadata not found: %w", err)
		}
		return nil, fmt.Errorf("read metadata: %w", err)
	}

	var metadata Metadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}
	return &metadata, nil
}

// SaveMetadata writes atomically: temp file -> fsync -> rename.
func SaveMetadata(path string, metadata *Metadata) error {
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}

	tempFile, err := os.CreateTemp(filepath.Dir(path), "metadata-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tempFile.Name())

	// os.CreateTemp makes a 0600 file, so restore the mode being replaced.
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := tempFile.Chmod(mode); err != nil {
		tempFile.Close()
		return err
	}
	if _, err := tempFile.Write(data); err != nil {
		return err
	}
	if err := tempFile.Sync(); err != nil {
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	return os.Rename(tempFile.Name(), path)
}
