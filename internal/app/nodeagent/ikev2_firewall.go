package nodeagent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const ikev2FirewallChain = "ANTIMAGE_IKEV2_INPUT"

func runIKEv2Firewall(
	ctx context.Context,
	args ...string,
) ([]byte, error) {
	path, err :=
		exec.LookPath("iptables")

	if err != nil {
		return nil, err
	}

	return exec.CommandContext(
		ctx,
		path,
		args...,
	).CombinedOutput()
}

func ensureIKEv2Firewall() error {
	ctx, cancel :=
		context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
	defer cancel()

	output, err := runIKEv2Firewall(
		ctx,
		"-w", "5",
		"-N",
		ikev2FirewallChain,
	)

	if err != nil {
		detail :=
			strings.ToLower(
				strings.TrimSpace(
					string(output),
				),
			)

		if !strings.Contains(
			detail,
			"chain already exists",
		) {
			return fmt.Errorf(
				"create IKEv2 firewall chain: %s",
				strings.TrimSpace(
					string(output),
				),
			)
		}
	}

	if _, err := runIKEv2Firewall(
		ctx,
		"-w", "5",
		"-F",
		ikev2FirewallChain,
	); err != nil {
		return err
	}

	for _, port := range []string{
		"500",
		"4500",
	} {
		if _, err := runIKEv2Firewall(
			ctx,
			"-w", "5",
			"-A",
			ikev2FirewallChain,
			"-p", "udp",
			"--dport", port,
			"-j", "ACCEPT",
		); err != nil {
			return fmt.Errorf(
				"allow IKEv2 UDP/%s: %w",
				port,
				err,
			)
		}
	}

	if _, err := runIKEv2Firewall(
		ctx,
		"-w", "5",
		"-C", "INPUT",
		"-j",
		ikev2FirewallChain,
	); err != nil {
		if _, err := runIKEv2Firewall(
			ctx,
			"-w", "5",
			"-I", "INPUT", "1",
			"-j",
			ikev2FirewallChain,
		); err != nil {
			return fmt.Errorf(
				"attach IKEv2 firewall chain: %w",
				err,
			)
		}
	}

	return nil
}

func cleanupIKEv2Firewall() {
	ctx, cancel :=
		context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
	defer cancel()

	_, _ = runIKEv2Firewall(
		ctx,
		"-w", "5",
		"-D", "INPUT",
		"-j",
		ikev2FirewallChain,
	)

	_, _ = runIKEv2Firewall(
		ctx,
		"-w", "5",
		"-F",
		ikev2FirewallChain,
	)

	_, _ = runIKEv2Firewall(
		ctx,
		"-w", "5",
		"-X",
		ikev2FirewallChain,
	)
}
