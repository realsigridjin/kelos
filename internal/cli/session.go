package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const sessionRuntimeClient = "/kelos/bin/kelos-session-runtime"

type sessionConnectDependencies struct {
	resolveConfig func() (*rest.Config, string, error)
	getSession    func(context.Context, *rest.Config, string, string) (*kelos.Session, error)
	connect       func(context.Context, *rest.Config, string, string, io.Reader, io.Writer, io.Writer, bool) error
}

func newSessionCommand(cfg *ClientConfig) *cobra.Command {
	command := &cobra.Command{
		Use:     "session",
		Aliases: []string{"sessions"},
		Short:   "Interact with Sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(newSessionConnectCommand(cfg))
	return command
}

func newSessionConnectCommand(cfg *ClientConfig) *cobra.Command {
	dependencies := sessionConnectDependencies{
		resolveConfig: cfg.resolveConfig,
		getSession: func(ctx context.Context, restConfig *rest.Config, namespace, name string) (*kelos.Session, error) {
			controllerClient, err := client.New(restConfig, client.Options{Scheme: scheme})
			if err != nil {
				return nil, fmt.Errorf("creating Kubernetes client: %w", err)
			}
			session := &kelos.Session{}
			if err := controllerClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, session); err != nil {
				return nil, err
			}
			return session, nil
		},
		connect: connectSessionPod,
	}
	return newSessionConnectCommandWithDependencies(cfg, dependencies)
}

func newSessionConnectCommandWithDependencies(cfg *ClientConfig, dependencies sessionConnectDependencies) *cobra.Command {
	colorMode := "auto"
	command := &cobra.Command{
		Use:   "connect NAME",
		Short: "Connect to a Session using terminal chat",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			color, err := terminalColorEnabled(colorMode, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			name := args[0]
			restConfig, namespace, err := dependencies.resolveConfig()
			if err != nil {
				return err
			}
			session, err := dependencies.getSession(cmd.Context(), restConfig, namespace, name)
			if err != nil {
				return fmt.Errorf("getting Session %q: %w", name, err)
			}
			if session.Status.Phase != kelos.SessionPhaseReady || session.Status.PodName == "" {
				return fmt.Errorf("Session %q is not ready (phase: %s)", name, session.Status.Phase)
			}
			if err := dependencies.connect(cmd.Context(), restConfig, namespace, session.Status.PodName, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), color); err != nil {
				return fmt.Errorf("connecting to Session %q: %w", name, err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&colorMode, "color", "auto", "Color output: auto, always, or never")
	command.ValidArgsFunction = completeSessionNames(cfg)
	return command
}

func terminalColorEnabled(mode string, output io.Writer) (bool, error) {
	switch mode {
	case "always":
		return true, nil
	case "never":
		return false, nil
	case "auto":
		if _, disabled := os.LookupEnv("NO_COLOR"); disabled || os.Getenv("TERM") == "dumb" {
			return false, nil
		}
		file, ok := output.(*os.File)
		if !ok {
			return false, nil
		}
		info, err := file.Stat()
		if err != nil {
			return false, fmt.Errorf("detecting terminal color support: %w", err)
		}
		return info.Mode()&os.ModeCharDevice != 0, nil
	default:
		return false, fmt.Errorf("invalid color mode %q: must be auto, always, or never", mode)
	}
}

func connectSessionPod(ctx context.Context, restConfig *rest.Config, namespace, podName string, stdin io.Reader, stdout, stderr io.Writer, color bool) error {
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("creating Kubernetes client: %w", err)
	}
	request := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("exec")
	request.VersionedParams(&corev1.PodExecOptions{
		Container: kelos.AgentContainerName,
		Command:   []string{sessionRuntimeClient, "client"},
		Stdin:     true,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}, clientgoscheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(restConfig, "POST", request.URL())
	if err != nil {
		return fmt.Errorf("creating exec connection: %w", err)
	}

	requestReader, requestWriter := io.Pipe()
	eventReader, eventWriter := io.Pipe()
	streamDone := make(chan error, 1)
	go func() {
		err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdin:  requestReader,
			Stdout: eventWriter,
			Stderr: stderr,
			Tty:    false,
		})
		_ = eventWriter.CloseWithError(err)
		_ = requestReader.CloseWithError(err)
		streamDone <- err
	}()

	terminalErr := runSessionTerminal(ctx, stdin, stdout, eventReader, requestWriter, color)
	_ = requestWriter.Close()
	_ = eventReader.Close()
	streamErr := <-streamDone
	if streamErr != nil {
		return streamErr
	}
	return terminalErr
}
