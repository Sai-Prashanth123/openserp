package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/karust/openserp/cmd"
	"github.com/sirupsen/logrus"
)

func main() {
	defer recoverPanic()

	// Handle graceful shutdown signals (SIGINT, SIGTERM)
	// This ensures browser instances are properly closed on Ctrl+C or container stop
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		logrus.Infof("Received signal %s, shutting down gracefully...", sig)
		os.Exit(0)
	}()

	if err := cmd.RootCmd.Execute(); err != nil {
		logrus.Info(err)
		os.Exit(1)
	}
}

func recoverPanic() {
	if r := recover(); r != nil {
		logrus.Fatalf("Error: %v\n", r)
	}
}
