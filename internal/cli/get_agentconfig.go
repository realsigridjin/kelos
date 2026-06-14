package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func newGetAgentConfigCommand(cfg *ClientConfig, allNamespaces *bool) *cobra.Command {
	var output string
	var detail bool

	cmd := &cobra.Command{
		Use:     "agentconfig [name]",
		Aliases: []string{"agentconfigs", "ac"},
		Short:   "List agent configs or get a specific agent config",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "" && output != "yaml" && output != "json" {
				return fmt.Errorf("unknown output format %q: must be one of yaml, json", output)
			}

			if *allNamespaces && len(args) == 1 {
				return fmt.Errorf("a resource cannot be retrieved by name across all namespaces")
			}

			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
			}

			ctx := context.Background()

			if len(args) == 1 {
				ac, outputObject, err := getAgentConfig(ctx, cl, client.ObjectKey{Name: args[0], Namespace: ns})
				if err != nil {
					return fmt.Errorf("getting agent config: %w", err)
				}

				switch output {
				case "yaml":
					return printYAML(os.Stdout, outputObject)
				case "json":
					return printJSON(os.Stdout, outputObject)
				default:
					if detail {
						printAgentConfigDetail(os.Stdout, ac)
					} else {
						printAgentConfigTable(os.Stdout, []kelos.AgentConfig{*ac}, false)
					}
					return nil
				}
			}

			var listOpts []client.ListOption
			if !*allNamespaces {
				listOpts = append(listOpts, client.InNamespace(ns))
			}
			items, outputList, err := listAgentConfigs(ctx, cl, listOpts...)
			if err != nil {
				return fmt.Errorf("listing agent configs: %w", err)
			}

			switch output {
			case "yaml":
				return printYAML(os.Stdout, outputList)
			case "json":
				return printJSON(os.Stdout, outputList)
			default:
				printAgentConfigTable(os.Stdout, items, *allNamespaces)
				return nil
			}
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (yaml or json)")
	cmd.Flags().BoolVarP(&detail, "detail", "d", false, "Show detailed information for a specific agent config")

	cmd.ValidArgsFunction = completeAgentConfigNames(cfg)
	_ = cmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions([]string{"yaml", "json"}, cobra.ShellCompDirectiveNoFileComp))

	return cmd
}
