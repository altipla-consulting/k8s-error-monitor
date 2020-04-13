package main

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	log "github.com/sirupsen/logrus"
	flag "github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"
	v1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
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
	var flagSentryDSN, flagEnv string
	flag.StringVarP(&flagSentryDSN, "sentry-dsn", "s", "", "Sentry DSN to send the errors. Required.")
	flag.StringVarP(&flagEnv, "env", "e", "production", "Name of the environment to report to Sentry.")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
	k, err := kubernetes.NewForConfig(config)
	if err != nil {
		return errors.Trace(err)
	}

	w := &watcher{
		k:   k,
		env: flagEnv,
		hub: hub,
	}

	g, ctx := errgroup.WithContext(ctx)
	g.Go(w.watchEvents(ctx))
	g.Go(func() error {
		watch.Interrupt(ctx, cancel)
		return nil
	})

	log.WithField("sentry-dsn", flagSentryDSN).Info("Capturing Kubernetes errors")
	if err := g.Wait(); err != nil {
		return errors.Trace(err)
	}

	return nil
}

func alerter(ctx context.Context) chan struct{} {
	ch := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch
}

type watcher struct {
	k   *kubernetes.Clientset
	env string
	hub *sentry.Hub
}

func (w *watcher) watchEvents(ctx context.Context) func() error {
	return func() error {
		watchList := cache.NewListWatchFromClient(w.k.CoreV1().RESTClient(), "events", v1.NamespaceAll, fields.Everything())
		_, controller := cache.NewInformer(watchList, new(v1.Event), time.Second*30, cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				if err := w.handleEvent(obj); err != nil {
					log.Fatal(errors.Stack(err))
				}
			},
		})
		controller.Run(alerter(ctx))
		return nil
	}
}

func (w *watcher) handleEvent(obj interface{}) error {
	kevent, ok := obj.(*v1.Event)
	if !ok {
		return errors.Errorf("unexpected event type: %#v", kevent)
	}

	if kevent.Type == v1.EventTypeNormal {
		return nil
	}

	if shouldDiscard(kevent) {
		return nil
	}

	event := sentry.NewEvent()
	event.Platform = "other"
	event.Environment = w.env
	event.Timestamp = kevent.ObjectMeta.CreationTimestamp.Unix()
	event.Level = getSentryLevel(kevent)
	event.Message = kevent.Message
	event.Exception = []sentry.Exception{
		{
			Type:  kevent.Message,
			Value: fmt.Sprintf("(%s/%s) %s", strings.ToLower(kevent.InvolvedObject.Kind), kevent.InvolvedObject.Name, kevent.Reason),
		},
	}

	event.Tags["namespace"] = kevent.InvolvedObject.Namespace
	event.Tags["component"] = kevent.Source.Component
	if kevent.Source.Host != "" {
		event.Tags["node"] = kevent.Source.Host
	}
	if kevent.ClusterName != "" {
		event.Tags["cluster"] = kevent.ClusterName
	}
	event.Tags["kind"] = kevent.InvolvedObject.Kind
	event.Tags["type"] = kevent.Type
	event.Tags["reason"] = kevent.Reason

	if kevent.Action != "" {
		event.Extra["action"] = kevent.Action
	}
	event.Extra["count"] = kevent.Count
	event.Extra["involved_object"] = kevent.InvolvedObject

	var err error
	event.Fingerprint, err = w.fingerprint(kevent)
	if err != nil {
		return errors.Trace(err)
	}

	fields := log.Fields{
		"namespace": event.Tags["namespace"],
		"component": event.Tags["component"],
		"kind":      event.Tags["kind"],
		"type":      event.Tags["type"],
		"reason":    event.Tags["reason"],
		"message":   event.Message,
	}
	if event.Tags["node"] != "" {
		fields["node"] = event.Tags["node"]
	}
	log.WithFields(fields).Info("Event")

	eventID := w.hub.CaptureEvent(event)
	log.WithField("event-id", eventID).Info("Error sent to Sentry")

	return nil
}

func (w *watcher) fingerprint(kevent *v1.Event) ([]string, error) {
	fp := []string{
		kevent.Source.Component,
		kevent.Type,
		kevent.Reason,
		kevent.Message,
	}
	if kevent.InvolvedObject.APIVersion == "v1" && kevent.InvolvedObject.Kind == "Pod" {
		pod, err := w.k.CoreV1().Pods(kevent.Namespace).Get(kevent.InvolvedObject.Name, metav1.GetOptions{
			ResourceVersion: kevent.InvolvedObject.ResourceVersion,
		})
		if err != nil {
			status := new(k8serrors.StatusError)
			if !errors.As(err, &status) || status.ErrStatus.Reason != "NotFound" {
				return nil, errors.Trace(err)
			}
		} else {
			fp = append(fp, fingerprintFromMeta(pod.ObjectMeta)...)
			return fp, nil
		}
	}

	fp = append(fp,
		kevent.InvolvedObject.APIVersion,
		kevent.InvolvedObject.Kind,
		kevent.InvolvedObject.Namespace,
		kevent.InvolvedObject.Name,
		kevent.InvolvedObject.FieldPath)
	return fp, nil
}

func getSentryLevel(kevent *v1.Event) sentry.Level {
	switch kevent.Type {
	case v1.EventTypeNormal:
		return sentry.LevelInfo
	case v1.EventTypeWarning:
		return sentry.LevelWarning
	case "Error":
		return sentry.LevelError
	default:
		return sentry.LevelError
	}
}

func fingerprintFromMeta(resource metav1.ObjectMeta) []string {
	// If the object has a controller owner, use that for grouping purposes.
	for _, owner := range resource.OwnerReferences {
		if owner.Controller != nil && *owner.Controller {
			return []string{
				owner.APIVersion,
				owner.Kind,
				owner.Name,
			}
		}
	}

	// Otherwise we group based onthe object itself
	return []string{
		resource.Namespace,
		string(resource.UID),
	}
}

func shouldDiscard(kevent *v1.Event) bool {
	// Esto ocurre mientras se mueve el volumen de una máquina a otra durante
	// un funcionamiento normal. Kubernetes reintentará el movimiento en unos segundos.
	if strings.HasSuffix(kevent.Message, "Volume is already exclusively attached to one node and can't be attached to another") {
		return true
	}

	// Kubernetes reintentará mas tarde sin problemas.
	if kevent.Reason == "FailedToUpdateEndpoint" {
		return true
	}

	// Esto ocurre mientras se reinician los pods en un funcionamiento normal para
	// sustituirse por los de la nueva versión.
	if strings.Contains(kevent.Message, "Liveness probe failed") {
		return true
	}

	return false
}
