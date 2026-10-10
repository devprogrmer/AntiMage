package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	systemapp "github.com/antimage/antimage/internal/app/system"
)

func TestInstalledServiceVersionIsIndependentOfRunningBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	t.Setenv("ANTIMAGE_NODE_BINARY_METADATA_FILE", path)
	if got := installedServiceVersion(); got != "" {
		t.Fatalf("missing metadata fabricated installed version: %q", got)
	}
	for _, test := range []struct{ payload, want string }{
		{`{"tag":"v1.2.3"}`, "v1.2.3"}, {`{"tag":"v1.2.4"}`, "v1.2.4"}, {`{broken`, ""}, {`{}`, ""},
	} {
		if err := os.WriteFile(path, []byte(test.payload), 0600); err != nil {
			t.Fatal(err)
		}
		if got := installedServiceVersion(); got != test.want {
			t.Fatalf("installed=%q,want %q", got, test.want)
		}
	}
}

func TestInstalledBinaryIdentityRequiresActualBytes(t *testing.T) {
	app := t.TempDir()
	if err := os.Mkdir(filepath.Join(app, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(app, "bin", "antimage-node")
	payload := []byte("exact immutable executable bytes")
	digest := sha256.Sum256(payload)
	target := systemapp.ResolvedInstall{BuildCatalogEntry: systemapp.BuildCatalogEntry{Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}}
	if err := os.WriteFile(binaryPath, payload, 0700); err != nil {
		t.Fatal(err)
	}
	if err := installedBinaryMatchesTarget(context.Background(), app, target); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, []byte("wrong immutable executable bytes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := installedBinaryMatchesTarget(context.Background(), app, target); err == nil {
		t.Fatal("incorrect installed bytes accepted")
	}
	if err := os.WriteFile(binaryPath, payload, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := installedBinaryMatchesTarget(ctx, app, target); err == nil {
		t.Fatal("cancelled binary verification accepted")
	}
	target.Size++
	if err := installedBinaryMatchesTarget(context.Background(), app, target); err == nil {
		t.Fatal("wrong installed size accepted")
	}
}
