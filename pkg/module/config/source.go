package config

// Document is a loaded module definition together with the canonical
// source locations the loader resolved for it. It is plain data:
// constructing it from a file is the loader package's job.
type Document struct {
	// Definition is the validated definition. Normalize has applied
	// placement defaults; authored strings such as the manifest path,
	// images, and env values are preserved verbatim.
	Definition PlatformModuleDefinition

	// DefinitionFile is the canonical, symlink-resolved path of the
	// definition file that was loaded.
	DefinitionFile string

	// ModuleRoot is the canonical directory containing the definition
	// file. Every renderer input must stay inside it.
	ModuleRoot string

	// RendererEntry is the canonical directory the manifest backend
	// selected: the Helm chart root, or the directory containing
	// kustomization.yaml.
	RendererEntry string
}
