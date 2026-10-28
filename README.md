# odh-platform-tools

Offline tooling for OpenDataHub platform module definitions: `module lint`
validates a definition and its rendered manifests against the packaging
rules the ODH operator enforces; `module deploy` renders and installs a
definition's controller package onto a cluster through the operator's own
rendering and deployment framework. No external renderer binaries,
containers, or credentials beyond the cluster connection.

## Quickstart

Lint a module (offline, no cluster):

```sh
go run github.com/lburgazzoli/odh-platform-tools/cmd module lint \
  --config examples/helm/chart/module.yaml
```

The bundled examples — `examples/helm` and `examples/kustomize` — are
complete, self-contained module roots using the co-located layout; their
images are non-sensitive placeholders.

Deploy a module:

```sh
go run github.com/lburgazzoli/odh-platform-tools/cmd module deploy \
  --config examples/helm/chart/module.yaml \
  --platform-version 2.19.0 \
  --namespace odh-example \
  --related-images-file examples/images.env \
  --wait
```

Behavior in short:

- One-shot installer: applies the rendered controller package; never
  creates the module CR or operands, never reconciles.
- No rollback: a failed deploy leaves the writes that preceded the
  failure standing.
- Default success means apply completion; `--wait` additionally waits for
  the controller Deployment rollout.

## Build

```sh
make build      # build all packages
make verify     # fmt-check, vet, lint, unit tests
```

## Documentation

- [Module definitions](docs/modules.md) — authoring a
  `PlatformModuleDefinition`
- [CLI reference](docs/cli.md) — commands, flags, reports, exit codes
- [Architecture](docs/architecture.md) — packages and pipelines
- [Development](docs/development.md) — layout, conventions, commands
- [Testing](docs/testing.md) — suites, style, e2e engines
