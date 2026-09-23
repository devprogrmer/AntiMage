package nodeagent

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type nativeSpeedDirection uint8

const (
	nativeSpeedUpload nativeSpeedDirection = iota
	nativeSpeedDownload

	nativeSpeedUploadPref   = 49152
	nativeSpeedDownloadPref = 49153

	nativeSpeedActionBase uint32 = 0xa0000000
	nativeSpeedMaxUserID  int64  = 0x07ffffff
)

var (
	nativeSpeedLimitRun = func(
		ctx context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}

	nativeSpeedLimitLookPath = exec.LookPath
	nativeSpeedLimitGOOS     = runtime.GOOS
)

func nativeSpeedActionIndex(
	userID int64,
	direction nativeSpeedDirection,
) (uint32, error) {
	if userID <= 0 {
		return 0, fmt.Errorf("speed limit user id must be positive")
	}
	if userID > nativeSpeedMaxUserID {
		return 0, fmt.Errorf(
			"speed limit user id %d exceeds supported range",
			userID,
		)
	}

	index := nativeSpeedActionBase + uint32(userID)*2
	if direction == nativeSpeedDownload {
		index++
	}

	return index, nil
}

func nativeSpeedBurstBytes(rate int64) int64 {
	burst := rate / 8 / 20

	if burst < 4096 {
		burst = 4096
	}

	const maxBurst = 4 * 1024 * 1024
	if burst > maxBurst {
		burst = maxBurst
	}

	return burst
}

func nativeSpeedRunTC(args ...string) ([]byte, error) {
	if nativeSpeedLimitGOOS != "linux" {
		return nil, fmt.Errorf(
			"native speed limiting is only supported on linux",
		)
	}

	tcPath, err := nativeSpeedLimitLookPath("tc")
	if err != nil {
		return nil, fmt.Errorf("tc command is not installed: %w", err)
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	output, err := nativeSpeedLimitRun(ctx, tcPath, args...)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}

		return output, fmt.Errorf(
			"tc %s: %s",
			strings.Join(args, " "),
			detail,
		)
	}

	return output, nil
}

func nativeSpeedEnsureClsact(interfaceName string) error {
	interfaceName = strings.TrimSpace(interfaceName)
	if interfaceName == "" {
		return fmt.Errorf("speed limit interface name is required")
	}

	output, err := nativeSpeedRunTC(
		"qdisc",
		"add",
		"dev",
		interfaceName,
		"clsact",
	)
	if err == nil {
		return nil
	}

	detail := strings.ToLower(strings.TrimSpace(string(output)))
	if strings.Contains(detail, "file exists") ||
		strings.Contains(detail, "exclusivity flag on") {
		return nil
	}

	return err
}

func nativeSpeedEnsureAction(
	userID int64,
	direction nativeSpeedDirection,
	rate int64,
) (uint32, error) {
	if rate <= 0 {
		return 0, nil
	}

	index, err := nativeSpeedActionIndex(userID, direction)
	if err != nil {
		return 0, err
	}

	_, err = nativeSpeedRunTC(
		"actions",
		"replace",
		"action",
		"police",
		"rate",
		strconv.FormatInt(rate, 10)+"bit",
		"burst",
		strconv.FormatInt(nativeSpeedBurstBytes(rate), 10)+"b",
		"conform-exceed",
		"drop/ok",
		"index",
		strconv.FormatUint(uint64(index), 10),
	)
	if err != nil {
		return 0, err
	}

	return index, nil
}

func nativeSpeedAttachIPv4(
	interfaceName string,
	address string,
	userID int64,
	uploadRate int64,
	downloadRate int64,
) error {
	addr, err := netip.ParseAddr(strings.TrimSpace(address))
	if err != nil || !addr.Is4() {
		return fmt.Errorf(
			"invalid speed limit IPv4 address %q",
			address,
		)
	}

	if uploadRate <= 0 && downloadRate <= 0 {
		return nil
	}

	if err := nativeSpeedEnsureClsact(interfaceName); err != nil {
		return err
	}

	if uploadRate > 0 {
		actionIndex, err := nativeSpeedEnsureAction(
			userID,
			nativeSpeedUpload,
			uploadRate,
		)
		if err != nil {
			return err
		}

		if _, err := nativeSpeedRunTC(
			"filter",
			"add",
			"dev",
			interfaceName,
			"ingress",
			"protocol",
			"ip",
			"pref",
			strconv.Itoa(nativeSpeedUploadPref),
			"u32",
			"match",
			"ip",
			"src",
			addr.String()+"/32",
			"action",
			"police",
			"index",
			strconv.FormatUint(uint64(actionIndex), 10),
		); err != nil {
			return err
		}
	}

	if downloadRate > 0 {
		actionIndex, err := nativeSpeedEnsureAction(
			userID,
			nativeSpeedDownload,
			downloadRate,
		)
		if err != nil {
			return err
		}

		if _, err := nativeSpeedRunTC(
			"filter",
			"add",
			"dev",
			interfaceName,
			"egress",
			"protocol",
			"ip",
			"pref",
			strconv.Itoa(nativeSpeedDownloadPref),
			"u32",
			"match",
			"ip",
			"dst",
			addr.String()+"/32",
			"action",
			"police",
			"index",
			strconv.FormatUint(uint64(actionIndex), 10),
		); err != nil {
			return err
		}
	}

	return nil
}

func nativeSpeedClearInterface(interfaceName string) {
	interfaceName = strings.TrimSpace(interfaceName)
	if interfaceName == "" || nativeSpeedLimitGOOS != "linux" {
		return
	}

	_, _ = nativeSpeedRunTC(
		"filter",
		"del",
		"dev",
		interfaceName,
		"ingress",
		"protocol",
		"ip",
		"pref",
		strconv.Itoa(nativeSpeedUploadPref),
	)

	_, _ = nativeSpeedRunTC(
		"filter",
		"del",
		"dev",
		interfaceName,
		"egress",
		"protocol",
		"ip",
		"pref",
		strconv.Itoa(nativeSpeedDownloadPref),
	)
}

func nativeSpeedDeleteActionIndex(index uint32) {
	if index == 0 || nativeSpeedLimitGOOS != "linux" {
		return
	}

	_, _ = nativeSpeedRunTC(
		"actions",
		"delete",
		"action",
		"police",
		"index",
		strconv.FormatUint(uint64(index), 10),
	)
}
func nativeSpeedDeleteAction(
	userID int64,
	direction nativeSpeedDirection,
) {
	index, err := nativeSpeedActionIndex(userID, direction)
	if err != nil || nativeSpeedLimitGOOS != "linux" {
		return
	}

	_, _ = nativeSpeedRunTC(
		"actions",
		"delete",
		"action",
		"police",
		"index",
		strconv.FormatUint(uint64(index), 10),
	)
}
