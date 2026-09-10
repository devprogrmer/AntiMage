package nodeagent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReconcileOpenVPNTProxyStartupFlushesStaleChainOnce(
	t *testing.T,
) {
	oldRun := openVPNNetworkRun
	oldLookPath := openVPNNetworkLookPath
	oldGOOS := openVPNNetworkGOOS

	defer func() {
		openVPNNetworkRun = oldRun
		openVPNNetworkLookPath = oldLookPath
		openVPNNetworkGOOS = oldGOOS
	}()

	openVPNNetworkGOOS = "linux"

	openVPNNetworkLookPath = func(
		name string,
	) (string, error) {
		return name, nil
	}

	var commands []string

	openVPNNetworkRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)

		if name == "iptables" &&
			strings.Contains(
				command,
				"-S "+openVPNTProxyChain,
			) {
			return []byte(
				"-N " + openVPNTProxyChain + "\n",
			), nil
		}

		return nil, nil
	}

	server := New(
		Config{
			DataDir: t.TempDir(),
		},
	)

	prepared := []preparedOpenVPNRuntime{
		{
			Tag: "openvpn-main",
			TProxy: openVPNTProxySpec{
				Enabled: true,
			},
		},
	}

	if err := server.reconcileOpenVPNTProxyStartup(
		prepared,
	); err != nil {
		t.Fatal(err)
	}

	if err := server.reconcileOpenVPNTProxyStartup(
		prepared,
	); err != nil {
		t.Fatal(err)
	}

	flushCount := 0

	for _, command := range commands {
		if strings.Contains(
			command,
			"-F "+openVPNTProxyChain,
		) {
			flushCount++
		}
	}

	if flushCount != 1 {
		t.Fatalf(
			"startup chain flush count=%d, want 1:\n%s",
			flushCount,
			strings.Join(commands, "\n"),
		)
	}
}

func TestReconcileOpenVPNTProxyStartupRetriesAfterFlushFailure(
	t *testing.T,
) {
	oldRun := openVPNNetworkRun
	oldLookPath := openVPNNetworkLookPath
	oldGOOS := openVPNNetworkGOOS

	defer func() {
		openVPNNetworkRun = oldRun
		openVPNNetworkLookPath = oldLookPath
		openVPNNetworkGOOS = oldGOOS
	}()

	openVPNNetworkGOOS = "linux"

	openVPNNetworkLookPath = func(
		name string,
	) (string, error) {
		return name, nil
	}

	failFlush := true

	openVPNNetworkRun = func(
		_ context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")

		if name == "iptables" &&
			strings.Contains(
				command,
				"-S "+openVPNTProxyChain,
			) {
			return []byte(
				"-N " + openVPNTProxyChain + "\n",
			), nil
		}

		if name == "iptables" &&
			strings.Contains(
				command,
				"-F "+openVPNTProxyChain,
			) &&
			failFlush {
			return nil, errors.New("flush failed")
		}

		return nil, nil
	}

	server := New(
		Config{
			DataDir: t.TempDir(),
		},
	)

	prepared := []preparedOpenVPNRuntime{
		{
			Tag: "openvpn-main",
			TProxy: openVPNTProxySpec{
				Enabled: true,
			},
		},
	}

	if err := server.reconcileOpenVPNTProxyStartup(
		prepared,
	); err == nil {
		t.Fatal("expected startup reconcile failure")
	}

	if server.openVPNTProxyStartupReconciled {
		t.Fatal(
			"failed startup reconcile was incorrectly marked complete",
		)
	}

	failFlush = false

	if err := server.reconcileOpenVPNTProxyStartup(
		prepared,
	); err != nil {
		t.Fatal(err)
	}

	if !server.openVPNTProxyStartupReconciled {
		t.Fatal(
			"successful retry was not marked reconciled",
		)
	}
}
