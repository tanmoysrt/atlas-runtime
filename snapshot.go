package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Snapshot is the record of one rootfs snapshot.
type Snapshot struct {
	ID         string `json:"id"`
	InstanceID string `json:"instance_id"`
	CreatedAt  string `json:"created_at"`
	Size       int64  `json:"size"` // logical bytes of the rootfs file
	Live       bool   `json:"live"` // the VM kept running during the copy
}

// newSnapshotID returns a short random identifier, for example
// "snap-k3f9x2mq7b".
func newSnapshotID() string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	randomBytes := make([]byte, 10)
	rand.Read(randomBytes)

	characters := make([]byte, len(randomBytes))
	for index, value := range randomBytes {
		characters[index] = alphabet[int(value)%len(alphabet)]
	}
	return "snap-" + string(characters)
}

// ListSnapshots reads the record of each snapshot in a directory. A directory
// that has no readable record is not a complete snapshot, and it is skipped.
func ListSnapshots(directory string) ([]Snapshot, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return []Snapshot{}, nil
		}
		return nil, err
	}

	snapshots := []Snapshot{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		snapshot, err := loadSnapshot(filepath.Join(directory, entry.Name()))
		if err != nil {
			continue
		}
		snapshots = append(snapshots, *snapshot)
	}
	return snapshots, nil
}

// DeleteSnapshot removes one snapshot directory. The filesystem frees only the
// extents that no other clone of the rootfs uses.
func DeleteSnapshot(directory, id string) error {
	if !safeName(id) {
		return fmt.Errorf("invalid snapshot id: %s", id)
	}
	path := filepath.Join(directory, id)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("snapshot not found: %s", id)
	}
	return os.RemoveAll(path)
}

// saveSnapshot writes the record of a snapshot into its directory.
func saveSnapshot(directory string, snapshot *Snapshot) error {
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(directory, "metadata.json"), data)
}

// loadSnapshot reads the record of a snapshot from its directory.
func loadSnapshot(directory string) (*Snapshot, error) {
	data, err := os.ReadFile(filepath.Join(directory, "metadata.json"))
	if err != nil {
		return nil, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// snapshotRootfsPath resolves a "<vm-id>/<snapshot-id>" reference to the
// rootfs file of that snapshot.
func snapshotRootfsPath(snapshotsDirectory, reference string) (string, error) {
	parts := strings.Split(reference, "/")
	if len(parts) != 2 || !safeName(parts[0]) || !safeName(parts[1]) {
		return "", fmt.Errorf("boot.snapshot must be \"<vm-id>/<snapshot-id>\": %s", reference)
	}
	return filepath.Join(snapshotsDirectory, parts[0], parts[1], "rootfs"), nil
}

// safeName reports whether a name is one path element that stays inside its
// directory.
func safeName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, character := range name {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case character == '-' || character == '_' || character == '.':
		default:
			return false
		}
	}
	return true
}
