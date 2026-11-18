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
  [--related-images-file related-images.yaml] [--wait] ...
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | — | Module definition file (required) |
| `--platform-version` | — | Platform version, shared by platform config and deploy (required) |
| `--related-images-file` | — | YAML file of related image sources (no automatic discovery) |
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

### Related-images file

`--related-images-file` accepts YAML regardless of the file name extension.
This is a breaking change for existing dotenv files. Convert assignments to
entries under `relatedImages`.

```yaml
relatedImages:
- name: RELATED_IMAGE_EXAMPLE_CONTROLLER
  value: quay.io/example/example-controller:v1.0.0
```

Entry names are case-sensitive. A matching process environment variable
takes precedence, and an explicitly empty variable masks its file value.
Missing, null, and empty entry values supply no file value. Omit the flag
to resolve sources from the environment alone.

The loader uses Viper's default decoding and keeps text such as `${VARIABLE}`
literal. File linting and image-reference validation are outside this format
migration.

## odh-platform-tools module mock

Runs the mock controller in the foreground until the command is cancelled.
It uses the inherited Kubernetes connection flags and watches a
discoverable, cluster-scoped custom resource. Its CRD supplies the
singleton constraint and a compatible platform status schema.

```
odh-platform-tools module mock \
  --api-version <group/version> --kind <Kind> --platform-configmap <name>
```

| Flag | Description |
|------|-------------|
| `--api-version` | Custom resource group and version (required) |
| `--kind` | Custom resource kind (required) |
| `--platform-configmap` | Name of the ConfigMap providing `data.platformVersion` (required) |
| `--namespace` | Optional inherited override for the ConfigMap namespace |

The ConfigMap namespace defaults to the Pod namespace in-cluster.
The example Deployments pass it as `POD_NAMESPACE` using the Downward
API. The in-cluster ServiceAccount namespace file is the fallback.
Outside the cluster, the inherited kubeconfig/context settings supply
the namespace, with `default` as the fallback.

Run one replica. Process health and readiness probes listen on port
8081. Status controls and the example startup procedure are documented
in the [mock walkthrough](mock.md).

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Runtime, connection, apply, wait, timeout, cancellation failures; lint error-severity findings |
| 2 | Usage, invalid options, definition, or image-file errors |

Cancellation (SIGINT/SIGTERM) is classified as exit 1.
