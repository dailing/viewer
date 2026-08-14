package pluginapi

import (
	"context"
	"errors"
	"os"

	"viewer/internal/plugins/agentcodex"
	"viewer/internal/plugins/agenthermes"
	"viewer/internal/plugins/agentopencode"
	"viewer/internal/plugins/chat"
	"viewer/internal/plugins/configstore"
	"viewer/internal/plugins/fileservice"
	"viewer/internal/plugins/gateway"
	"viewer/internal/plugins/inspector"
	"viewer/internal/plugins/instancestore"
	"viewer/internal/plugins/supervisor"
	"viewer/internal/plugins/terminal"
)

// Registry is the complete resident core set. Inspector starts first so it can
// observe as much of the remaining boot traffic as possible.
var Registry = []Entry{
	{ID: inspector.Manifest.ID, Factory: newInspector},
	{ID: configstore.Manifest.ID, Factory: newConfigStore},
	{ID: agenthermes.Manifest.ID, Factory: newAgentHermes},
	{ID: agentcodex.Manifest.ID, Factory: newAgentCodex},
	{ID: agentopencode.Manifest.ID, Factory: newAgentOpenCode},
	{ID: instancestore.Manifest.ID, Factory: newInstanceStore},
	{ID: fileservice.Manifest.ID, Factory: newFileService},
	{ID: chat.Manifest.ID, Factory: newChat},
	{ID: terminal.Manifest.ID, Factory: newTerminal},
	{ID: supervisor.Manifest.ID, Factory: newSupervisor},
	{ID: "gateway", Factory: newGateway},
}

func newAgentOpenCode(config RuntimeConfig) (Plugin, error) {
	plugin := agentopencode.New()
	return lifecycleAdapter{
		start: func(ctx context.Context) error { return plugin.Start(ctx, config.KernelWS, false) },
		wait:  waitContext, close: func(context.Context) error { return plugin.Close() },
	}, nil
}

func newAgentCodex(config RuntimeConfig) (Plugin, error) {
	plugin := agentcodex.New()
	return lifecycleAdapter{
		start: func(ctx context.Context) error { return plugin.Start(ctx, config.KernelWS, false) },
		wait:  waitContext, close: func(context.Context) error { return plugin.Close() },
	}, nil
}

func newAgentHermes(config RuntimeConfig) (Plugin, error) {
	plugin := agenthermes.New()
	return lifecycleAdapter{
		start: func(ctx context.Context) error { return plugin.Start(ctx, config.KernelWS, false) },
		wait:  waitContext, close: func(context.Context) error { return plugin.Close() },
	}, nil
}

func newChat(config RuntimeConfig) (Plugin, error) {
	plugin, err := chat.New(config.DataDir)
	if err != nil {
		return nil, err
	}
	return lifecycleAdapter{
		start: func(ctx context.Context) error { return plugin.Start(ctx, config.KernelWS, false) },
		wait:  waitContext, close: func(context.Context) error { return plugin.Close() },
	}, nil
}

type lifecycleAdapter struct {
	start func(context.Context) error
	wait  func(context.Context) error
	close func(context.Context) error
}

func (a lifecycleAdapter) Start(ctx context.Context) error { return a.start(ctx) }
func (a lifecycleAdapter) Wait(ctx context.Context) error  { return a.wait(ctx) }
func (a lifecycleAdapter) Close(ctx context.Context) error { return a.close(ctx) }

func waitContext(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func newInspector(config RuntimeConfig) (Plugin, error) {
	plugin, err := inspector.New(inspector.Config{KernelWS: config.KernelWS})
	if err != nil {
		return nil, err
	}
	return lifecycleAdapter{
		start: func(ctx context.Context) error { return plugin.StartWithManaged(ctx, false) }, wait: plugin.Wait,
		close: func(context.Context) error { return plugin.Close() },
	}, nil
}

func newConfigStore(config RuntimeConfig) (Plugin, error) {
	plugin, err := configstore.New(dataPath(config, "config.json"))
	if err != nil {
		return nil, err
	}
	return lifecycleAdapter{
		start: func(ctx context.Context) error { return plugin.Start(ctx, config.KernelWS, false) },
		wait:  waitContext, close: func(context.Context) error { return plugin.Close() },
	}, nil
}

func newInstanceStore(config RuntimeConfig) (Plugin, error) {
	plugin, err := instancestore.New(dataPath(config, "instance.json"))
	if err != nil {
		return nil, err
	}
	return lifecycleAdapter{
		start: func(ctx context.Context) error { return plugin.Start(ctx, config.KernelWS, false) },
		wait:  waitContext, close: func(context.Context) error { return plugin.Close() },
	}, nil
}

func newFileService(config RuntimeConfig) (Plugin, error) {
	plugin := fileservice.New()
	return lifecycleAdapter{
		start: func(ctx context.Context) error { return plugin.Start(ctx, config.KernelWS, false) },
		wait:  waitContext, close: func(context.Context) error { return plugin.Close() },
	}, nil
}

func newTerminal(config RuntimeConfig) (Plugin, error) {
	plugin := terminal.New(config.KernelWS)
	return lifecycleAdapter{start: plugin.Start, wait: waitContext, close: plugin.Close}, nil
}

func newSupervisor(config RuntimeConfig) (Plugin, error) {
	registryPath := dataPath(config, "registry.json")
	if err := ensureEmptyRegistry(registryPath); err != nil {
		return nil, err
	}
	defaults := supervisor.DefaultConfig()
	plugin, err := supervisor.New(supervisor.Config{
		KernelWS: config.KernelWS, RegistryPath: registryPath,
		LogDir: dataPath(config, "logs"), BackoffBase: defaults.BackoffBase,
	})
	if err != nil {
		return nil, err
	}
	return lifecycleAdapter{
		start: func(ctx context.Context) error { return plugin.StartWithManaged(ctx, false) }, wait: waitContext,
		close: func(context.Context) error { plugin.Close(); return nil },
	}, nil
}

func ensureEmptyRegistry(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = file.WriteString("{\n  \"plugins\": []\n}\n"); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func newGateway(config RuntimeConfig) (Plugin, error) {
	serverConfig := gateway.DefaultConfig()
	serverConfig.KernelWS, serverConfig.Host, serverConfig.Port = config.KernelWS, config.GatewayHost, config.GatewayPort
	serverConfig.StaticFS = config.StaticFS
	server := gateway.New(serverConfig)
	return lifecycleAdapter{
		start: func(context.Context) error { return server.Start() },
		wait:  func(context.Context) error { return server.Wait() },
		close: server.Shutdown,
	}, nil
}
