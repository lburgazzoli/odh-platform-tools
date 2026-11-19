# Module definitions

A module is described by one `PlatformModuleDefinition`
(`config.opendatahub.io/v1alpha1`, cluster-scoped) YAML file plus a local
manifest source (Helm chart or Kustomize tree). The bundled packages
install controllers for `HelmModule` and `KustomizeModule`. Each instance
manifest sits beside the package and is applied separately after deploy,
then `module validate` checks the running module against the platform
contract. [Run the bundled examples](mock.md)

## Skeleton

This definition matches the bundled Helm controller package.

```yaml
apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: helm-module
spec:
  instance:                       # identity of the separately applied module CR
    name: default-helm-module     # optional; defaults to metadata.name
    type:                         # the module CR type the controller watches
      group: components.platform.opendatahub.io
      version: v1alpha1
      kind: HelmModule
  manifests:                      # exactly one backend
    format: helm                  # helm | kustomize
    helm:
      values:                     # literal values merged over chart defaults
        replicas: 1
  relatedImages:                  # image source keys resolved at deploy time
  - RELATED_IMAGE_MOCK_CONTROLLER
  controller:
    deployment:
      name: mock-controller      # optional; defaults to metadata.name
      container:
        image: '{{RELATED_IMAGE_MOCK_CONTROLLER}}'
        env:
        - name: LOG_LEVEL
          value: debug
```

For Kustomize: `manifests.format: kustomize` and optionally
`manifests.kustomize.path`. An empty `path` uses the definition file's own
directory in both backends.

## Generating a definition

`module generate` infers this file from an existing chart or Kustomize tree
instead of writing it by hand, then the normal lint and deploy workflow
applies unchanged:

```sh
# Helm: write next to (not inside) the chart
odh-platform-tools module generate examples/helm/chart \
  --output examples/helm/generated.yaml
odh-platform-tools module lint --config examples/helm/generated.yaml

# Kustomize: colocated output is allowed
odh-platform-tools module generate examples/kustomize \
  --output examples/kustomize/generated.yaml
odh-platform-tools module lint --config examples/kustomize/generated.yaml
```

Both invocations produce a definition whose `manifests` path resolves
relative to the generated file, so lint works from any directory. To deploy,
follow the [`module deploy`](cli.md#odh-platform-tools-module-deploy)
instructions — the platform version, connection inputs, and any
`--related-images-file` are still required.

Discovery is strict: the source must render exactly one CRD with a single
served storage version (it supplies the module GVK and, lowercased, the
default name) and exactly one controller Deployment whose main container is
the unique `manager` or the only regular container. Init containers require
an explicit `--init-container` name. Value overrides passed with
`--values-file`/`--values` are persisted in the generated `helm.values`, and
`--related-images-file` copies only the image source names. See the
[CLI reference](cli.md#odh-platform-tools-module-generate) for flags,
policies, and failure classes.

The generated file is a starting point: edit it freely (extra env, an
`{{VARIABLE}}` image token, a different instance name) and keep it in sync
with the manifests yourself — regeneration never merges into an existing
file.

## Fields

| Field | Notes |
|-------|-------|
| `spec.instance.name` | Defaults to `metadata.name` |
| `spec.instance.type` | Required GVK of the module CR |
| `spec.manifests.format` | `helm` or `kustomize`; the other backend block must be absent |
| `spec.manifests.helm.values` | Literal JSON values; no interpolation |
| `spec.relatedImages` | Names of image sources to inject as environment variables. Values come from the process environment or the YAML file selected by `--related-images-file`. |
| `spec.controller.deployment.name` | Selects the controller Deployment; defaults to `metadata.name` |
| `spec.controller.deployment.container` | Main container; `name` defaults to `manager` |
| `spec.controller.deployment.container.image` | Literal or whole `{{VARIABLE}}` token; must resolve at deploy |
| `spec.controller.deployment.initContainer` | Required name; optional `image` (literal or whole `{{VARIABLE}}`); empty uses the main image |

## Loading rules

- The file must contain exactly one document.
- Decoding is permissive: unknown fields are dropped; type mismatches on
  mapped fields and duplicate keys fail.
- Placement defaults (`instance.name`, container `name`) are applied by the
  loader after decode.
- `module lint` validates offline; `module deploy` renders, injects images
  and platform config, and installs the controller package; after the
  instance is created separately, `module validate` checks the running
  module read-only with automatic waiting.

## Checks (lint)

- The bundle contains exactly one Deployment, and the configured controller
  Deployment and its selected containers exist.
- The bundle contains exactly one CRD declaring the module's own group/kind.
- Every bundled object's kind is on the module manifest allowlist.
