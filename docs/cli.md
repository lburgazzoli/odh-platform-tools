# CLI reference

Binary: `odh-platform-tools`. Command group: `module`.

## odh-platform-tools module lint

Loads one definition, renders its manifest backend offline, validates the
rendered objects, and writes a report to stdout. No cluster access.

```
odh-platform-tools module lint --config <definition.yaml> [--output text|json]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | — | Module definition file (required) |
| `--output` | `text` | Report format: `text` or `json` |

Report: status (`pass` | `findings` | `failed`), diagnostics with severity,
and on execution failure a classified `failure` block. Error-severity
findings exit 1 without an execution error.

## odh-platform-tools module deploy

Loads one definition, renders and adapts it, publishes the definition owner
(CRD + object), and applies the controller package through the framework
deploy action. One-shot installer; it never creates the module CR.

```
odh-platform-tools module deploy \
  --config <definition.yaml> \
  --platform-version <semver> \
  [--related-images-file images.env] [--wait] ...
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | — | Module definition file (required) |
| `--platform-version` | — | Platform version, shared by platform config and deploy (required) |
| `--related-images-file` | — | Dotenv file of image sources; no file is discovered automatically |
| `--monitoring-namespace` | — | Injected as `MONITORING_NAMESPACE` |
| `--platform-type` | `OpenDataHub` | Injected as `ODH_MODULE_OPERATOR_PLATFORM_TYPE` |
| `--field-manager` | `platform.opendatahub.io` | SSA field manager (≤63 chars, label value) |
| `--timeout` | `5m` | Overall invocation budget, connection included |
| `--wait` | `false` | Wait for the controller Deployment rollout after apply |
| `--wait-timeout` | `30s` | Shared rollout wait budget; capped by remaining `--timeout` |
| `--output` | `text` | Report format: `text` or `json` |

Standard kubeconfig/context/namespace/auth flags are inherited from the root
command (kubectl semantics: explicit flag, else context namespace, else
`default`).

Report: status (`applied` | `failed`), confirmed owner identity, attempted
and completed stages, `applyCompleted`, wait state, and classified
`failure`. A missing owner means unconfirmed, not that no write occurred.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Runtime, connection, apply, wait, timeout, cancellation failures; lint error-severity findings |
| 2 | Usage, invalid options, definition, or image-file errors |

Cancellation (SIGINT/SIGTERM) is classified as exit 1.
