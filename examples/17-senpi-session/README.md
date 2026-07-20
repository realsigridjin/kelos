# Senpi session

This example creates a persistent senpi conversation in a dedicated
StatefulSet-backed Session. The senpi image must be built and pushed first:

```sh
make image REGISTRY=ghcr.io/realsigridjin WHAT=senpi PUSH=true
docker push ghcr.io/realsigridjin/senpi:latest
```

Create a Secret with the provider API key expected by senpi:

```sh
kubectl create secret generic senpi-credentials \
  --from-literal=SENPI_API_KEY=<provider-api-key>
```

Create a matching Workspace, then apply the Session:

```sh
kubectl apply -f workspace.yaml
kubectl apply -f session.yaml
kelos get session senpi-session
kelos session connect senpi-session
```

The Session uses the `SENPI_CODING_AGENT_DIR` path on its persistent workspace
and receives `AgentConfig.spec.agentsMD` at `~/.senpi/agent/AGENTS.md`.
