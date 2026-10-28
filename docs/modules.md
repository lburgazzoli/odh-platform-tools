# Module definitions

A module is described by one `PlatformModuleDefinition`
(`config.opendatahub.io/v1alpha1`, cluster-scoped) YAML file plus a local
manifest source (Helm chart or Kustomize tree). Working examples:
`examples/helm` and `examples/kustomize`.

## Skeleton

```yaml
apiVersion: config.opendatahub.io/v1alpha1
kind: PlatformModuleDefinition
metadata:
  name: example
spec:
  instance:                       # identity of the module CR (never created by this tool)
    name: default-example         # optional; defaults to metadata.name
    type:                         # the Module CR the controller owns
      group: components.platform.opendatahub.io
      version: v1alpha1
      kind: Example
  manifests:                      # exactly one backend
    format: helm                  # helm | kustomize
    helm:
      values:                     # literal values merged over chart defaults
        replicas: 2
  relatedImages:                  # image source keys resolved at deploy time
  - RELATED_IMAGE_EXAMPLE_CONTROLLER
  controller:
    deployment:
      name: example-controller    # optional; defaults to metadata.name
      container:
        image: quay.io/example/example-controller:v1.0.0   # literal or whole {{VARIABLE}}
        env:
        - name: LOG_LEVEL
          value: debug
      initContainer:              # optional; name + optional image
        name: init
        image: '{{RELATED_IMAGE_INIT}}'   # optional; empty uses the main image
```

For Kustomize: `manifests.format: kustomize` and optionally
`manifests.kustomize.path`. An empty `path` uses the definition file's own
directory in both backends.

## Fields

| Field | Notes |
|-------|-------|
| `spec.instance.name` | Defaults to `metadata.name` |
| `spec.instance.type` | Required GVK of the module CR |
| `spec.manifests.format` | `helm` or `kustomize`; the other backend block must be absent |
| `spec.manifests.helm.values` | Literal JSON values; no interpolation |
| `spec.relatedImages` | Image source keys; resolved from environment over the `--related-images-file` dotenv file |
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
  and platform config, and installs the controller package.

## Checks (lint)

- The bundle contains exactly one Deployment, and the configured controller
  Deployment and its selected containers exist.
- The bundle contains exactly one CRD declaring the module's own group/kind.
- Every bundled object's kind is on the module manifest allowlist.
