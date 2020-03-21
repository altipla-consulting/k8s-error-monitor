package main

import (
	"context"
	"os"
	"os/user"
	"path/filepath"

	"github.com/getsentry/sentry-go"
	log "github.com/sirupsen/logrus"
	flag "github.com/spf13/pflag"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"libs.altipla.consulting/errors"
	"libs.altipla.consulting/watch"
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

	sentryClient, err := sentry.NewClient(sentry.ClientOptions{
		Dsn: flagSentryDSN,
	})
	if err != nil {
		return errors.Trace(err)
	}
	hub := sentry.NewHub(sentryClient, sentry.NewScope())

	var config *rest.Config
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		config, err = rest.InClusterConfig()
	} else {
		usr, err := user.Current()
		if err != nil {
			return errors.Trace(err)
		}
		config, err = clientcmd.BuildConfigFromFlags("", filepath.Join(usr.HomeDir, ".kube", "config"))
		if err != nil {
			return errors.Trace(err)
		}
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return errors.Trace(err)
	}

	_ = hub
	_ = client

	log.WithField("sentry-dsn", flagSentryDSN).Info("Capturing Kubernetes errors")
	ctx, cancel := context.WithCancel(ctx)
	watch.Interrupt(ctx, cancel)

	return nil
}
