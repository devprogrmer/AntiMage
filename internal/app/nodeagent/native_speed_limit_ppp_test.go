package nodeagent

import (
	"context"
	"strings"
	"testing"
)

func TestNativeSpeedPPPSessionStartAppliesSharedLimits(t *testing.T) {
	oldRun := nativeSpeedLimitRun
	oldLookPath := nativeSpeedLimitLookPath
	oldGOOS := nativeSpeedLimitGOOS

	defer func() {
		nativeSpeedLimitRun = oldRun
		nativeSpeedLimitLookPath = oldLookPath
		nativeSpeedLimitGOOS = oldGOOS
	}()

	nativeSpeedLimitGOOS = "linux"

	nativeSpeedLimitLookPath = func(name string) (string, error) {
		return "/usr/sbin/" + name, nil
	}

	var commands []string

	nativeSpeedLimitRun = func(
		ctx context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		commands = append(
			commands,
			name+" "+strings.Join(args, " "),
		)
		return nil, nil
	}

	shaped, err := nativeSpeedHandlePPPSessionEvent(
		"l2tp",
		"start",
		"ppp7",
		"10.70.0.2",
		271,
		nativeSessionUserPolicy{
			UploadSpeedLimit:   200000,
			DownloadSpeedLimit: 300000,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if !shaped {
		t.Fatal("expected PPP session to be shaped")
	}

	joined := strings.Join(commands, "\n")

	for _, expected := range []string{
		"qdisc add dev ppp7 clsact",
		"match ip src 10.70.0.2/32",
		"match ip dst 10.70.0.2/32",
		"rate 200000bit",
		"rate 300000bit",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf(
				"missing %q in tc commands:\n%s",
				expected,
				joined,
			)
		}
	}
}

func TestNativeSpeedPPPSessionStopClearsOnlySessionFilters(t *testing.T) {
	oldRun := nativeSpeedLimitRun
	oldLookPath := nativeSpeedLimitLookPath
	oldGOOS := nativeSpeedLimitGOOS

	defer func() {
		nativeSpeedLimitRun = oldRun
		nativeSpeedLimitLookPath = oldLookPath
		nativeSpeedLimitGOOS = oldGOOS
	}()

	nativeSpeedLimitGOOS = "linux"

	nativeSpeedLimitLookPath = func(name string) (string, error) {
		return "/usr/sbin/" + name, nil
	}

	var commands []string

	nativeSpeedLimitRun = func(
		ctx context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		commands = append(
			commands,
			name+" "+strings.Join(args, " "),
		)
		return nil, nil
	}

	shaped, err := nativeSpeedHandlePPPSessionEvent(
		"pptp",
		"stop",
		"ppp9",
		"10.71.0.5",
		271,
		nativeSessionUserPolicy{
			UploadSpeedLimit:   200000,
			DownloadSpeedLimit: 300000,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if shaped {
		t.Fatal("stop event must not report active shaping")
	}

	joined := strings.Join(commands, "\n")

	if !strings.Contains(
		joined,
		"filter del dev ppp9 ingress",
	) {
		t.Fatalf(
			"PPP ingress filter was not cleared:\n%s",
			joined,
		)
	}

	if !strings.Contains(
		joined,
		"filter del dev ppp9 egress",
	) {
		t.Fatalf(
			"PPP egress filter was not cleared:\n%s",
			joined,
		)
	}

	if strings.Contains(
		joined,
		"actions delete action police",
	) {
		t.Fatalf(
			"PPP stop must not delete shared user actions:\n%s",
			joined,
		)
	}
}

func TestNativeSpeedPPPUnlimitedDoesNothing(t *testing.T) {
	oldRun := nativeSpeedLimitRun
	oldGOOS := nativeSpeedLimitGOOS

	defer func() {
		nativeSpeedLimitRun = oldRun
		nativeSpeedLimitGOOS = oldGOOS
	}()

	nativeSpeedLimitGOOS = "linux"

	called := false

	nativeSpeedLimitRun = func(
		ctx context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		called = true
		return nil, nil
	}

	shaped, err := nativeSpeedHandlePPPSessionEvent(
		"l2tp",
		"start",
		"ppp1",
		"10.70.0.10",
		271,
		nativeSessionUserPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if shaped {
		t.Fatal("unlimited PPP session must not be shaped")
	}

	if called {
		t.Fatal("unlimited PPP session must not invoke tc")
	}
}
