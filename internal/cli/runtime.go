package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/yoockh/sebab/internal/config"
	"github.com/yoockh/sebab/internal/diagnose"
	"github.com/yoockh/sebab/internal/engine"
	"github.com/yoockh/sebab/internal/platform"
	linuxplat "github.com/yoockh/sebab/internal/platform/linux"
	"github.com/yoockh/sebab/internal/probe"
	"github.com/yoockh/sebab/internal/secret"
	"github.com/yoockh/sebab/internal/version"
)

// hooks let tests replace the parts that touch the real system.
type hooks struct {
	network platform.Network
	dialer  probe.Dialer
	keyring secret.Keyring
	canary  []string // overrides the configured canary targets
	dnsName []string // overrides the configured DNS canary names
	clock   engine.Clock
	jitter  bool // keep first-check jitter (default off in tests)
}

// runtime is the shared wiring for commands that probe or touch secrets.
type runtime struct {
	env      Env
	paths    config.Paths
	redactor *secret.Redactor
	store    *secret.Store
	network  platform.Network
	prober   *probe.Prober
}

func userAgent() string { return "sebab/" + version.Version + " (+https://github.com/yoockh/sebab)" }

func newRuntime(env Env) (*runtime, error) {
	paths, err := config.ResolvePaths(env.Getenv)
	if err != nil {
		return nil, err
	}
	rt := &runtime{env: env, paths: paths, redactor: secret.NewRedactor()}
	var kr secret.Keyring = secret.SystemKeyring{}
	if env.hooks != nil && env.hooks.keyring != nil {
		kr = env.hooks.keyring
	}
	rt.store = &secret.Store{Keyring: kr, FilePath: paths.SecretsFile(), Getenv: env.Getenv}
	rt.network = linuxplat.NewNetwork()
	if env.hooks != nil && env.hooks.network != nil {
		rt.network = env.hooks.network
	}
	rt.prober = probe.New(rt.network, userAgent())
	if env.hooks != nil && env.hooks.dialer != nil {
		rt.prober.Dialer = env.hooks.dialer
	}
	// The failure lab serves its own DNS on loopback; this lets the real binary
	// resolve *.lab.test names during manual lab sessions. Never set in normal use.
	if dns := env.Getenv("SEBAB_LAB_DNS"); dns != "" {
		rt.prober.Resolver.Server = dns
	}
	return rt, nil
}

// canary builds a canary checker from the engine settings. Test hooks pin the
// targets, and the pinned checker ignores config reloads.
func (rt *runtime) canary(ec config.Engine) engine.Canary {
	c := &probe.CanaryChecker{Prober: rt.prober, Targets: ec.Canary, DNSNames: ec.DNSCanary,
		Timeout: 3 * time.Second, CacheFor: 15 * time.Second}
	if rt.env.hooks != nil && rt.env.hooks.canary != nil {
		c.Targets, c.DNSNames = rt.env.hooks.canary, rt.env.hooks.dnsName
		return pinnedCanary{c}
	}
	return c
}

// pinnedCanary hides CanaryChecker.Configure from the engine.
type pinnedCanary struct{ c *probe.CanaryChecker }

func (p pinnedCanary) Check(ctx context.Context, withDNS bool) diagnose.Canary {
	return p.c.Check(ctx, withDNS)
}
func (p pinnedCanary) Invalidate() { p.c.Invalidate() }

func (rt *runtime) clock() engine.Clock {
	if rt.env.hooks != nil && rt.env.hooks.clock != nil {
		return rt.env.hooks.clock
	}
	return engine.RealClock{}
}

// loadConfig reads the config; a missing file yields an empty config.
func (rt *runtime) loadConfig() (*config.Config, error) {
	return config.LoadOrEmpty(rt.paths.ConfigFile)
}

// ctxTimeout is a small helper for one-shot operations.
func ctxTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}

func errorf(env Env, format string, a ...any) int {
	fmt.Fprintf(env.Stderr, "sebab: "+format+"\n", a...)
	return ExitInternal
}
