package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	systemapp "github.com/antimage/antimage/internal/app/system"
)

// Missing installer metadata means unknown; it must never be replaced with the
// build version of the running process.
func installedServiceVersion() string {
	path := strings.TrimSpace(os.Getenv("ANTIMAGE_NODE_BINARY_METADATA_FILE"))
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var metadata struct {
		Tag string `json:"tag"`
	}
	if json.Unmarshal(data, &metadata) != nil {
		return ""
	}
	return strings.TrimSpace(metadata.Tag)
}

// Verify the actual installed bytes, rather than treating a metadata tag as
// proof that an interrupted atomic install reached its intended target.
func installedBinaryMatchesTarget(ctx context.Context, appDir string, target systemapp.ResolvedInstall) error {
	if appDir == "" || target.Size <= 0 || len(target.SHA256) != 64 {
		return fmt.Errorf("installed binary identity is unavailable")
	}
	root, err := filepath.EvalSymlinks(appDir)
	if err != nil {
		return err
	}
	binaryPath := filepath.Join(root, "bin", "antimage-node")
	info, err := os.Lstat(binaryPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() != target.Size {
		return fmt.Errorf("installed binary size or file type does not match target")
	}
	resolved, err := filepath.EvalSymlinks(binaryPath)
	if err != nil || resolved != binaryPath {
		return fmt.Errorf("installed binary path is not the managed regular file")
	}
	file, err := os.Open(binaryPath)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := file.Read(buffer)
		if count > 0 {
			_, _ = hash.Write(buffer[:count])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(target.SHA256) {
		return fmt.Errorf("installed binary checksum does not match target")
	}
	return nil
}
