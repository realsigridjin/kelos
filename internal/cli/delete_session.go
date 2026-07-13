package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func newDeleteSessionCommand(cfg *ClientConfig) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:     "session [name]",
		Aliases: []string{"sessions", "sess"},
		Short:   "Delete a session",
		Args: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return fmt.Errorf("cannot specify session name with --all")
			}
			if !all {
				if len(args) == 0 {
					return fmt.Errorf("session name is required (or use --all)\nUsage: %s", cmd.Use)
				}
				if len(args) > 1 {
					return fmt.Errorf("too many arguments: expected 1 session name, got %d\nUsage: %s", len(args), cmd.Use)
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, namespace, err := cfg.NewClient()
			if err != nil {
				return err
			}
			return runDeleteSession(cmd.Context(), cl, namespace, args, all, cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Delete all sessions in the namespace")
	cmd.ValidArgsFunction = completeSessionNames(cfg)

	return cmd
}

func runDeleteSession(ctx context.Context, cl client.Client, namespace string, args []string, all bool, out io.Writer) error {
	if all {
		sessionList := &kelos.SessionList{}
		if err := cl.List(ctx, sessionList, client.InNamespace(namespace)); err != nil {
			return fmt.Errorf("listing sessions: %w", err)
		}
		if len(sessionList.Items) == 0 {
			fmt.Fprintln(out, "No sessions found")
			return nil
		}
		for i := range sessionList.Items {
			if err := cl.Delete(ctx, &sessionList.Items[i]); err != nil {
				return fmt.Errorf("deleting session %s: %w", sessionList.Items[i].Name, err)
			}
			fmt.Fprintf(out, "session/%s deleted\n", sessionList.Items[i].Name)
		}
		return nil
	}

	name := args[0]
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	if err := cl.Delete(ctx, session); err != nil {
		return fmt.Errorf("deleting session %s: %w", name, err)
	}
	fmt.Fprintf(out, "session/%s deleted\n", name)
	return nil
}
