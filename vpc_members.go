package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// vpcMember is one remote VM, cached so its route can be rebuilt when the
// host starts again before beacon is reachable.
type vpcMember struct {
	GreAddress string `json:"gre_address"`
}

// loadVPCMembers reads the cached members. A missing file gives an empty map.
func loadVPCMembers(path string) (map[string]vpcMember, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]vpcMember{}, nil
		}
		return nil, err
	}
	var members map[string]vpcMember
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, err
	}
	return members, nil
}

// saveVPCMembers writes atomically: temp file -> fsync -> rename.
func saveVPCMembers(path string, members map[string]vpcMember) error {
	data, err := json.MarshalIndent(members, "", "  ")
	if err != nil {
		return err
	}

	tempFile, err := os.CreateTemp(filepath.Dir(path), "vpc-members-*.json")
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
