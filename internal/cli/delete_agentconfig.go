package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newDeleteAgentConfigCommand(cfg *ClientConfig) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:     "agentconfig [name]",
		Aliases: []string{"agentconfigs", "ac"},
		Short:   "Delete an agent config",
		Args: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return fmt.Errorf("cannot specify agent config name with --all")
			}
			if !all {
				if len(args) == 0 {
					return fmt.Errorf("agent config name is required (or use --all)\nUsage: %s", cmd.Use)
				}
				if len(args) > 1 {
					return fmt.Errorf("too many arguments: expected 1 agent config name, got %d\nUsage: %s", len(args), cmd.Use)
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
			}

			ctx := context.Background()

			if all {
				items, _, err := listAgentConfigs(ctx, cl, client.InNamespace(ns))
				if err != nil {
					return fmt.Errorf("listing agent configs: %w", err)
				}
				if len(items) == 0 {
					fmt.Fprintln(os.Stdout, "No agent configs found")
					return nil
				}
				for i := range items {
					if err := deleteAgentConfig(ctx, cl, items[i].Name, items[i].Namespace); err != nil {
						return fmt.Errorf("deleting agent config %s: %w", items[i].Name, err)
					}
					fmt.Fprintf(os.Stdout, "agentconfig/%s deleted\n", items[i].Name)
				}
				return nil
			}

			if err := deleteAgentConfig(ctx, cl, args[0], ns); err != nil {
				return fmt.Errorf("deleting agent config %s: %w", args[0], err)
			}
			fmt.Fprintf(os.Stdout, "agentconfig/%s deleted\n", args[0])
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Delete all agent configs in the namespace")
	cmd.ValidArgsFunction = completeAgentConfigNames(cfg)

	return cmd
}
