package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/kelos-dev/kelos/internal/codexauth"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func main() {
	var namespace string
	var secretName string
	flag.StringVar(&namespace, "namespace", "", "Namespace of the Codex OAuth Secret to refresh.")
	flag.StringVar(&secretName, "secret", "", "Name of the Codex OAuth Secret to refresh.")
	flag.Parse()
	if namespace == "" || secretName == "" {
		fmt.Fprintln(os.Stderr, "Error: --namespace and --secret are required")
		os.Exit(1)
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating in-cluster Kubernetes config: %v\n", err)
		os.Exit(1)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating Kubernetes client: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := codexauth.Run(ctx, clientset, codexauth.Options{Namespace: namespace, SecretName: secretName}); err != nil {
		fmt.Fprintf(os.Stderr, "Error refreshing Codex OAuth credentials: %v\n", err)
		os.Exit(1)
	}
}
