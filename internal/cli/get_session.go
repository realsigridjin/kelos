package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func newGetSessionCommand(cfg *ClientConfig, allNamespaces *bool) *cobra.Command {
	var output string
	var detail bool

	cmd := &cobra.Command{
		Use:     "session [name]",
		Aliases: []string{"sessions", "sess"},
		Short:   "List sessions or get a specific session",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "" && output != "yaml" && output != "json" {
				return fmt.Errorf("unknown output format %q: must be one of yaml, json", output)
			}
			if *allNamespaces && len(args) == 1 {
				return fmt.Errorf("a resource cannot be retrieved by name across all namespaces")
			}

			cl, namespace, err := cfg.NewClient()
			if err != nil {
				return err
			}
			return runGetSession(cmd.Context(), cl, namespace, args, *allNamespaces, output, detail, cmd.OutOrStdout())
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (yaml or json)")
	cmd.Flags().BoolVarP(&detail, "detail", "d", false, "Show detailed information for a specific session")
	cmd.ValidArgsFunction = completeSessionNames(cfg)
	_ = cmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions([]string{"yaml", "json"}, cobra.ShellCompDirectiveNoFileComp))

	return cmd
}

func runGetSession(
	ctx context.Context,
	cl client.Client,
	namespace string,
	args []string,
	allNamespaces bool,
	output string,
	detail bool,
	out io.Writer,
) error {
	if len(args) == 1 {
		name := args[0]
		session := &kelos.Session{}
		if err := cl.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, session); err != nil {
			return fmt.Errorf("getting session %s: %w", name, err)
		}

		session.SetGroupVersionKind(kelos.GroupVersion.WithKind("Session"))
		switch output {
		case "yaml":
			return printYAML(out, session)
		case "json":
			return printJSON(out, session)
		default:
			if detail {
				printSessionDetail(out, session)
			} else {
				printSessionTable(out, []kelos.Session{*session}, false)
			}
			return nil
		}
	}

	sessionList := &kelos.SessionList{}
	var listOptions []client.ListOption
	if !allNamespaces {
		listOptions = append(listOptions, client.InNamespace(namespace))
	}
	if err := cl.List(ctx, sessionList, listOptions...); err != nil {
		return fmt.Errorf("listing sessions: %w", err)
	}

	sessionList.SetGroupVersionKind(kelos.GroupVersion.WithKind("SessionList"))
	switch output {
	case "yaml":
		return printYAML(out, sessionList)
	case "json":
		return printJSON(out, sessionList)
	default:
		printSessionTable(out, sessionList.Items, allNamespaces)
		return nil
	}
}
