package main

import (
	"context"

	"github.com/getsentry/sentry-go"
	"libs.altipla.consulting/watch"
  log "github.com/sirupsen/logrus"
  "libs.altipla.consulting/errors"
  flag "github.com/spf13/pflag"
)

func main() {
  if err := run(); err != nil {
    log.Fatal(errors.Stack(err))
  }
}

func run() error {
	var flagSentryDSN string
	flag.StringVarP(&flagSentryDSN, "sentry-dsn", "s", "", "Sentry DSN to send the errors. Required.")
	flag.Parse()

	ctx := context.Background()

	if flagSentryDSN == "" {
		return errors.Errorf("--sentry-dsn flag required")
	}

	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn: flagSentryDSN,
	})
	if err != nil {
	  return errors.Trace(err)
	}
	hub := sentry.NewHub(client, sentry.NewScope())

	_ = hub

	log.WithField("sentry-dsn", flagSentryDSN).Info("Capturing Kubernetes errors")
	ctx, cancel := context.WithCancel(ctx)
	watch.Interrupt(ctx, cancel)

  return nil
}
