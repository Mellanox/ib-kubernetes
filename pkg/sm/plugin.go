package sm

type PluginLoader interface {
	// LoadPlugin load go plugin from given path, and return error if failed.
	LoadPlugin(path string) error
}

type pluginLoader struct {
}

func NewPluginLoader() PluginLoader {
	return &pluginLoader{}
}

func (p *pluginLoader) LoadPlugin(path string) error {
	return nil
}
