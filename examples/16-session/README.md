# Interactive Session

This example creates one Claude Code conversation in a dedicated Pod. Replace
the API key placeholder, then apply both files:

```bash
kubectl apply -f examples/16-session/
kubectl get session interactive-review -w
```

When the Session is `Ready`, connect from a terminal:

```bash
kelos session connect interactive-review
```

The terminal client prints commands when interaction is required. Use
`/interrupt` to stop active work and `/answer INPUT_ID QUESTION_ID VALUE` for a
provider question.

The same conversation is available in the Session web application when the
shared Session server is enabled. Disconnecting either client does not stop the
agent runtime. Deleting the Session deletes its Pod and ends the conversation:

```bash
kubectl delete session interactive-review
```

To use another supported provider, set `spec.worker.type` to `codex` or
`opencode` and provide a credentials Secret with the corresponding
`CODEX_API_KEY` or `OPENCODE_API_KEY` key.
