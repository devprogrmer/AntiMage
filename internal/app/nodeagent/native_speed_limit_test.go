package nodeagent

import (
	"context"
	"strings"
	"testing"
)

func TestNativeSpeedActionIndexSharedPerUser(t *testing.T) {
	uploadA, err := nativeSpeedActionIndex(271, nativeSpeedUpload)
	if err != nil {
		t.Fatal(err)
	}

	uploadB, err := nativeSpeedActionIndex(271, nativeSpeedUpload)
	if err != nil {
		t.Fatal(err)
	}

	download, err := nativeSpeedActionIndex(271, nativeSpeedDownload)
	if err != nil {
		t.Fatal(err)
	}

	otherUser, err := nativeSpeedActionIndex(272, nativeSpeedUpload)
	if err != nil {
		t.Fatal(err)
	}

	if uploadA != uploadB {
		t.Fatalf(
			"same user upload action differs: %d != %d",
			uploadA,
			uploadB,
		)
	}

	if uploadA == download {
		t.Fatal("upload and download actions must differ")
	}

	if uploadA == otherUser {
		t.Fatal("different users must not share action index")
	}
}

func TestNativeSpeedAttachIPv4ReusesSharedAction(t *testing.T) {
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

	const userID int64 = 271

	if err := nativeSpeedAttachIPv4(
		"amwg-test",
		"10.69.0.54",
		userID,
		200000,
		200000,
	); err != nil {
		t.Fatal(err)
	}

	if err := nativeSpeedAttachIPv4(
		"awg-test",
		"192.168.0.115",
		userID,
		200000,
		200000,
	); err != nil {
		t.Fatal(err)
	}

	uploadIndex, err := nativeSpeedActionIndex(
		userID,
		nativeSpeedUpload,
	)
	if err != nil {
		t.Fatal(err)
	}

	downloadIndex, err := nativeSpeedActionIndex(
		userID,
		nativeSpeedDownload,
	)
	if err != nil {
		t.Fatal(err)
	}

	uploadToken := "index " + uint32String(uploadIndex)
	downloadToken := "index " + uint32String(downloadIndex)

	uploadReferences := 0
	downloadReferences := 0

	for _, command := range commands {
		if strings.Contains(command, uploadToken) {
			uploadReferences++
		}

		if strings.Contains(command, downloadToken) {
			downloadReferences++
		}
	}

	if uploadReferences < 4 {
		t.Fatalf(
			"expected shared upload action to be reused, references=%d commands=%v",
			uploadReferences,
			commands,
		)
	}

	if downloadReferences < 4 {
		t.Fatalf(
			"expected shared download action to be reused, references=%d commands=%v",
			downloadReferences,
			commands,
		)
	}
}

func TestNativeSpeedAttachIPv4UnlimitedDoesNothing(t *testing.T) {
	oldRun := nativeSpeedLimitRun
	oldLookPath := nativeSpeedLimitLookPath
	oldGOOS := nativeSpeedLimitGOOS

	defer func() {
		nativeSpeedLimitRun = oldRun
		nativeSpeedLimitLookPath = oldLookPath
		nativeSpeedLimitGOOS = oldGOOS
	}()

	nativeSpeedLimitGOOS = "linux"

	called := false

	nativeSpeedLimitLookPath = func(name string) (string, error) {
		return "/usr/sbin/" + name, nil
	}

	nativeSpeedLimitRun = func(
		ctx context.Context,
		name string,
		args ...string,
	) ([]byte, error) {
		called = true
		return nil, nil
	}

	if err := nativeSpeedAttachIPv4(
		"amwg-test",
		"10.69.0.54",
		271,
		0,
		0,
	); err != nil {
		t.Fatal(err)
	}

	if called {
		t.Fatal("unlimited user must not invoke tc")
	}
}

func uint32String(value uint32) string {
	const digits = "0123456789"

	if value == 0 {
		return "0"
	}

	var buf [10]byte
	pos := len(buf)

	for value > 0 {
		pos--
		buf[pos] = digits[value%10]
		value /= 10
	}

	return string(buf[pos:])
}
