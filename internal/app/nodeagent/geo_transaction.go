package nodeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	nodev1 "github.com/antimage/antimage/internal/proto/node/v1"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"
)

type geoTransaction struct {
	OperationID        string            `json:"operation_id"`
	CommandID          string            `json:"command_id"`
	ResourceGeneration int64             `json:"resource_generation"`
	RequestSHA256      string            `json:"request_sha256"`
	Phase              string            `json:"phase"`
	Deadline           int64             `json:"deadline_at_unix_nanos"`
	Files              map[string]string `json:"file_sha256"`
	NodeStartedAt      int64             `json:"node_started_at_unix_nano"`
	CoreStartedAt      int64             `json:"core_started_at_unix_nano"`
	ReloadBoundary     int64             `json:"reload_boundary_unix_nano"`
	ConfigSHA256       string            `json:"config_sha256"`
}

func geoTransactionPath(app, id string) (string, error) {
	if app == "" || !serviceNamePattern.MatchString(id) || len(id) > 96 {
		return "", fmt.Errorf("persistent Geo operation identity required")
	}
	return filepath.Join(app, ".maintenance-fences", "geo-"+id+".json"), nil
}

func readGeoTransaction(path string) (geoTransaction, error) {
	var tx geoTransaction
	info, err := os.Lstat(path)
	if err != nil {
		return tx, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return tx, fmt.Errorf("unsafe Geo transaction file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return tx, err
	}
	err = json.Unmarshal(data, &tx)
	return tx, err
}

func writeGeoTransaction(path string, tx geoTransaction) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe Geo transaction directory")
	}
	file, err := os.CreateTemp(dir, ".geo-transaction-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = json.NewEncoder(file).Encode(tx); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer d.Close()
		return d.Sync()
	}
	return nil
}

func geoFileHash(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 128<<20 {
		return "", fmt.Errorf("invalid Geo dataset file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(file, 128<<20+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func geoDatasetIdentity(tx geoTransaction, assets string) (string, error) {
	if len(tx.Files) == 0 || len(tx.Files) > 2 {
		return "", fmt.Errorf("Geo file identity unavailable")
	}
	names := make([]string, 0, len(tx.Files))
	for name := range tx.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		if name != "geoip.dat" && name != "geosite.dat" {
			return "", fmt.Errorf("invalid Geo dataset name")
		}
		actual, err := geoFileHash(filepath.Join(assets, name))
		if err != nil {
			return "", err
		}
		if actual != tx.Files[name] {
			return "", fmt.Errorf("Geo committed identity mismatch")
		}
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00", name, actual)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Server) reloadCommittedGeo(ctx context.Context, path string, tx geoTransaction) (*nodev1.RuntimeActionResponse, error) {
	if time.Now().UnixNano() >= tx.Deadline {
		return nil, fmt.Errorf("original Geo deadline expired; manual recovery required")
	}
	if tx.Phase != "files_committed" && tx.Phase != "reload_completed" && tx.Phase != "replacement_ready" {
		return nil, fmt.Errorf("Geo files not committed; reload-only recovery rejected")
	}
	if _, err := geoDatasetIdentity(tx, s.cfg.XrayAssetsDir); err != nil {
		return nil, err
	}
	// A crash can occur after the final atomic rename but before the commit
	// marker. Matching every intended file proves the missing step is reload.
	if tx.Phase == "replacement_ready" {
		tx.Phase = "files_committed"
		if err := writeGeoTransaction(path, tx); err != nil {
			return nil, err
		}
	}
	if tx.Phase == "reload_completed" {
		return nil, fmt.Errorf("Geo reload already completed; reconcile without replay")
	}
	bounded, cancel := context.WithDeadline(ctx, time.Unix(0, tx.Deadline))
	defer cancel()
	s.mu.Lock()
	configPath := s.lastConfig
	s.mu.Unlock()
	if configPath == "" {
		return nil, fmt.Errorf("Geo reload requires an active runtime configuration")
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	configDigest := sha256.Sum256(configBytes)
	tx.ConfigSHA256 = hex.EncodeToString(configDigest[:])
	tx.ReloadBoundary = time.Now().UnixNano()
	tx.NodeStartedAt = s.startedAt.UnixNano()
	tx.Phase = "reload_dispatched"
	if err := writeGeoTransaction(path, tx); err != nil {
		return nil, err
	}
	if err := s.startXray(bounded, configPath); err != nil {
		return nil, err
	}
	state := s.runtimeState("Geo reload completed")
	if !state.Started || state.CoreProcessStartedAtUnixNano <= 0 {
		return nil, fmt.Errorf("Geo reload has no running process evidence")
	}
	tx.Phase = "reload_completed"
	tx.NodeStartedAt = state.ProcessStartedAtUnixNano
	tx.CoreStartedAt = state.CoreProcessStartedAtUnixNano
	if err := writeGeoTransaction(path, tx); err != nil {
		return nil, err
	}
	return s.action(tx.OperationID, "Geo reload completed"), nil
}

func (s *Server) addGeoEvidence(req *nodev1.HealthRequest, state *nodev1.RuntimeState) {
	path, err := geoTransactionPath(os.Getenv("ANTIMAGE_NODE_APP_DIR"), req.OperationId)
	if err != nil {
		return
	}
	tx, err := readGeoTransaction(path)
	if err != nil {
		return
	}
	if tx.OperationID != req.OperationId || tx.CommandID != req.CommandId || tx.ResourceGeneration > state.CurrentResourceGeneration || state.EvidenceCommandState == "superseded" {
		return
	}
	state.EvidenceResourceGeneration = tx.ResourceGeneration
	state.GeoTransactionPhase = tx.Phase
	if tx.Phase == "downloading" && len(tx.Files) == 0 {
		state.GeoTransactionPhase = "files_not_committed"
		return
	}
	identity, err := geoDatasetIdentity(tx, s.cfg.XrayAssetsDir)
	if err != nil {
		state.GeoTransactionPhase = "identity_mismatch"
		return
	}
	state.GeoDatasetSha256 = identity
	if tx.Phase == "replacement_ready" {
		state.GeoTransactionPhase = "files_committed"
	}
	state.GeoReloadVerified = tx.Phase == "reload_completed" && state.Started && state.ProcessStartedAtUnixNano == tx.NodeStartedAt && state.CoreProcessStartedAtUnixNano == tx.CoreStartedAt
	// If activation succeeded before its transaction ACK was persisted, inspect
	// the actual fresh process/config identity instead of restarting it again.
	if tx.Phase == "reload_dispatched" && state.Started && tx.ReloadBoundary > 0 && tx.ConfigSHA256 != "" && state.ActiveConfigSha256 == tx.ConfigSHA256 && state.ProcessStartedAtUnixNano == tx.NodeStartedAt && state.CoreProcessStartedAtUnixNano > tx.ReloadBoundary && state.CoreProcessStartedAtUnixNano <= state.SampledAtUnixNano {
		state.GeoReloadVerified = true
		state.GeoTransactionPhase = "reload_completed"
	}
}
