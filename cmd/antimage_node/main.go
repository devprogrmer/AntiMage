package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/antimage/antimage/internal/app/logging"
	"github.com/antimage/antimage/internal/app/nodeagent"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "session-event" {
		if err := nodeagent.RunNativeSessionEventHelper(
			os.Args[2:],
		); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	cfg := nodeagent.LoadConfig()

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	logging.Infof(
		logging.ComponentNode,
		"node agent listening on %s:%d",
		cfg.ListenHost,
		cfg.ServicePort,
	)

	if err := nodeagent.New(cfg).Run(ctx); err != nil {
		logging.Fatalf(
			logging.ComponentNode,
			"node agent failed: %v",
			err,
		)
	}

	fmt.Fprintln(os.Stdout, "AntiMage-node stopped")
}
