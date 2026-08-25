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

// vpcMembersFile is the on-disk cache: the members plus the highest beacon
// timestamp (the revision) that they reflect. The revision lets the watcher
// resume with `since` instead of downloading every member again.
type vpcMembersFile struct {
	Revision int64                `json:"revision"`
	Members  map[string]vpcMember `json:"members"`
}

// loadVPCMembers reads the cached members. A missing file gives an empty cache
// with revision zero.
func loadVPCMembers(path string) (int64, map[string]vpcMember, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, map[string]vpcMember{}, nil
		}
		return 0, nil, err
	}
	var file vpcMembersFile
	if err := json.Unmarshal(data, &file); err != nil {
		return 0, nil, err
	}
	if file.Members == nil {
		file.Members = map[string]vpcMember{}
	}
	return file.Revision, file.Members, nil
}

// saveVPCMembers writes atomically: temp file -> fsync -> rename.
func saveVPCMembers(path string, revision int64, members map[string]vpcMember) error {
	data, err := json.MarshalIndent(vpcMembersFile{Revision: revision, Members: members}, "", "  ")
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
