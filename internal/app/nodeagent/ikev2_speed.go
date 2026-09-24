package nodeagent

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const ikev2SpeedTable = "antimage_ikev2_speed"

type ikev2SpeedBinding struct {
	UserID       int64
	IPv4         string
	UploadRate   int64
	DownloadRate int64
}

func ikev2SpeedBytesPerSecond(
	bitsPerSecond int64,
) uint64 {
	if bitsPerSecond <= 0 {
		return 0
	}

	value :=
		(uint64(bitsPerSecond) + 7) / 8

	const maxNFTLimit = uint64(^uint32(0))

	if value > maxNFTLimit {
		value = maxNFTLimit
	}

	if value == 0 {
		value = 1
	}

	return value
}

func normalizeIKEv2SpeedBindings(
	input []ikev2SpeedBinding,
) []ikev2SpeedBinding {
	seen := map[string]ikev2SpeedBinding{}

	for _, item := range input {
		if item.UserID <= 0 {
			continue
		}

		addr, err :=
			netip.ParseAddr(
				strings.TrimSpace(
					item.IPv4,
				),
			)

		if err != nil ||
			!addr.Is4() {
			continue
		}

		item.IPv4 = addr.String()

		key :=
			strconv.FormatInt(
				item.UserID,
				10,
			) + "|" + item.IPv4

		seen[key] = item
	}

	result := make(
		[]ikev2SpeedBinding,
		0,
		len(seen),
	)

	for _, item := range seen {
		result = append(
			result,
			item,
		)
	}

	sort.Slice(
		result,
		func(i int, j int) bool {
			if result[i].UserID !=
				result[j].UserID {
				return result[i].UserID <
					result[j].UserID
			}

			return result[i].IPv4 <
				result[j].IPv4
		},
	)

	return result
}

func renderIKEv2SpeedRules(
	input []ikev2SpeedBinding,
) string {
	bindings :=
		normalizeIKEv2SpeedBindings(
			input,
		)

	var b strings.Builder

	fmt.Fprintf(
		&b,
		"table inet %s {\n",
		ikev2SpeedTable,
	)

	b.WriteString(
		"  chain upload {\n" +
			"    type filter hook prerouting priority 0; policy accept;\n",
	)

	for _, item := range bindings {
		if item.UploadRate <= 0 {
			continue
		}

		fmt.Fprintf(
			&b,
			"    ip saddr %s limit rate over %d bytes/second counter drop\n",
			item.IPv4,
			ikev2SpeedBytesPerSecond(
				item.UploadRate,
			),
		)
	}

	b.WriteString("  }\n")

	b.WriteString(
		"  chain download {\n" +
			"    type filter hook postrouting priority 0; policy accept;\n",
	)

	for _, item := range bindings {
		if item.DownloadRate <= 0 {
			continue
		}

		fmt.Fprintf(
			&b,
			"    ip daddr %s limit rate over %d bytes/second counter drop\n",
			item.IPv4,
			ikev2SpeedBytesPerSecond(
				item.DownloadRate,
			),
		)
	}

	b.WriteString("  }\n")
	b.WriteString("}\n")

	return b.String()
}

func clearIKEv2SpeedLimits() {
	path, err := exec.LookPath("nft")
	if err != nil {
		return
	}

	ctx, cancel :=
		context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
	defer cancel()

	_, _ = exec.CommandContext(
		ctx,
		path,
		"delete",
		"table",
		"inet",
		ikev2SpeedTable,
	).CombinedOutput()
}

func reconcileIKEv2SpeedLimits(
	input []ikev2SpeedBinding,
) error {
	bindings :=
		normalizeIKEv2SpeedBindings(
			input,
		)

	clearIKEv2SpeedLimits()

	hasLimit := false

	for _, item := range bindings {
		if item.UploadRate > 0 ||
			item.DownloadRate > 0 {
			hasLimit = true
			break
		}
	}

	if !hasLimit {
		return nil
	}

	path, err := exec.LookPath("nft")
	if err != nil {
		return fmt.Errorf(
			"nft not installed: %w",
			err,
		)
	}

	ctx, cancel :=
		context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
	defer cancel()

	cmd := exec.CommandContext(
		ctx,
		path,
		"-f",
		"-",
	)

	cmd.Stdin = strings.NewReader(
		renderIKEv2SpeedRules(
			bindings,
		),
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(
			string(output),
		)

		if detail == "" {
			detail = err.Error()
		}

		return fmt.Errorf(
			"apply IKEv2 speed limits: %s",
			detail,
		)
	}

	return nil
}
