//go:build linux

package nodeagent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/antimage/antimage/internal/thirdparty/awgsrc"
)

const amneziaWGModule = "amneziawg"

var (
	amneziaWGProvisionMu sync.Mutex
	amneziaWGRunCommand  = func(name string, args ...string) ([]byte, error) {
		cmd := exec.Command(name, args...)
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		return cmd.CombinedOutput()
	}
	amneziaWGKernelRelease = func() (string, error) {
		out, err := exec.Command("uname", "-r").Output()
		return strings.TrimSpace(string(out)), err
	}
)

func ensureAmneziaWGProvisioned() error {
	amneziaWGProvisionMu.Lock()
	defer amneziaWGProvisionMu.Unlock()
	if amneziaWGModuleLoaded() {
		return nil
	}
	if _, err := exec.LookPath("apt-get"); err != nil {
		return fmt.Errorf("unsupported distribution: automatic AmneziaWG DKMS provisioning currently requires apt-get")
	}
	kernel, err := amneziaWGKernelRelease()
	if err != nil || kernel == "" {
		return fmt.Errorf("detect running kernel for AmneziaWG DKMS: %w", err)
	}
	headersDir := filepath.Join("/lib/modules", kernel, "build")
	if _, err := os.Stat(headersDir); err != nil {
		if commandErr := amneziaWGRun("apt-get", "update"); commandErr != nil {
			return commandErr
		}
		if commandErr := amneziaWGRun("apt-get", "install", "-y", "dkms", "build-essential", "linux-headers-"+kernel); commandErr != nil {
			return fmt.Errorf("install DKMS prerequisites or running-kernel headers: %w", commandErr)
		}
	} else if _, err := exec.LookPath("dkms"); err != nil {
		if commandErr := amneziaWGRun("apt-get", "update"); commandErr != nil {
			return commandErr
		}
		if commandErr := amneziaWGRun("apt-get", "install", "-y", "dkms", "build-essential"); commandErr != nil {
			return commandErr
		}
	}
	buildDir := filepath.Join("/usr/src", "antimage-amneziawg-"+awgsrc.Version)
	if _, err := os.Stat(filepath.Join(buildDir, ".antimage-source-version")); err != nil {
		if err := os.RemoveAll(buildDir); err != nil {
			return fmt.Errorf("replace AmneziaWG source: %w", err)
		}
		if err := awgsrc.Extract(buildDir); err != nil {
			return fmt.Errorf("extract bundled AmneziaWG source: %w", err)
		}
		if err := os.WriteFile(filepath.Join(buildDir, ".antimage-source-version"), []byte(awgsrc.Version+"\n"), 0644); err != nil {
			return err
		}
	}
	status, _ := amneziaWGRunCommand("dkms", "status", "-m", amneziaWGModule, "-v", awgsrc.Version, "-k", kernel)
	if !strings.Contains(string(status), "installed") {
		if err := amneziaWGRun("make", "-C", buildDir, "dkms-install"); err != nil {
			return err
		}
		if err := amneziaWGRunAllowAlreadyAdded("dkms", "add", "-m", amneziaWGModule, "-v", awgsrc.Version); err != nil {
			return err
		}
		if err := amneziaWGRun("dkms", "build", "-m", amneziaWGModule, "-v", awgsrc.Version, "-k", kernel); err != nil {
			return err
		}
		if err := amneziaWGRun("dkms", "install", "-m", amneziaWGModule, "-v", awgsrc.Version, "-k", kernel); err != nil {
			return err
		}
	}
	if err := amneziaWGRun("modprobe", amneziaWGModule); err != nil {
		return fmt.Errorf("load amneziawg kernel module (Secure Boot may reject an unsigned DKMS module): %w", err)
	}
	if !amneziaWGModuleLoaded() {
		return fmt.Errorf("amneziawg DKMS installation completed but the kernel module is not loaded; check Secure Boot and dmesg")
	}
	if err := os.MkdirAll("/etc/modules-load.d", 0755); err == nil {
		_ = atomicWriteFile("/etc/modules-load.d/amneziawg.conf", []byte("amneziawg\n"), 0644)
	}
	return nil
}

func amneziaWGModuleLoaded() bool {
	_, err := os.Stat("/sys/module/" + amneziaWGModule)
	return err == nil
}

func amneziaWGRun(name string, args ...string) error {
	out, err := amneziaWGRunCommand(name, args...)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, detail)
}

func amneziaWGRunAllowAlreadyAdded(name string, args ...string) error {
	out, err := amneziaWGRunCommand(name, args...)
	if err == nil || strings.Contains(strings.ToLower(string(out)), "already") {
		return nil
	}
	return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
}
