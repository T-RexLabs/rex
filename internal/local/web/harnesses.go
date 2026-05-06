package web

import (
	"context"
	"sync"
	"time"

	"github.com/asabla/rex/internal/core/runner/adapter"
)

type harnessFormOption struct {
	Name   string
	Models []string
	Modes  []string
}

type harnessCache struct {
	mu      sync.RWMutex
	options []harnessFormOption
}

func newHarnessCache(reg *adapter.Registry, ctx context.Context, workspaceRoot string, warm bool) *harnessCache {
	reg = normalizeHarnessRegistry(reg)
	c := &harnessCache{options: staticHarnessOptions(reg)}
	if !warm {
		return c
	}
	if ctx == nil {
		ctx = context.Background()
	}
	go c.refresh(ctx, reg, workspaceRoot)
	return c
}

func normalizeHarnessRegistry(reg *adapter.Registry) *adapter.Registry {
	if reg != nil {
		return reg
	}
	return adapter.Default()
}

func staticHarnessOptions(reg *adapter.Registry) []harnessFormOption {
	names := reg.Names()
	out := make([]harnessFormOption, 0, len(names))
	for _, name := range names {
		a, ok := reg.Lookup(name)
		if !ok {
			continue
		}
		caps := a.Capabilities()
		out = append(out, harnessFormOption{
			Name:   name,
			Models: append([]string(nil), caps.Models...),
			Modes:  append([]string(nil), caps.Modes...),
		})
	}
	return out
}

func (c *harnessCache) snapshot() []harnessFormOption {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]harnessFormOption, len(c.options))
	for i, opt := range c.options {
		out[i] = harnessFormOption{
			Name:   opt.Name,
			Models: append([]string(nil), opt.Models...),
			Modes:  append([]string(nil), opt.Modes...),
		}
	}
	return out
}

func (c *harnessCache) refresh(ctx context.Context, reg *adapter.Registry, workspaceRoot string) {
	updated := staticHarnessOptions(reg)
	for i := range updated {
		a, ok := reg.Lookup(updated[i].Name)
		if !ok {
			continue
		}
		d, ok := a.(adapter.DynamicCapabilities)
		if !ok {
			continue
		}
		discoveryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		dyn, err := d.Discover(discoveryCtx, adapter.DiscoverOptions{Dir: workspaceRoot})
		cancel()
		if err != nil {
			continue
		}
		if len(dyn.Models) > 0 {
			updated[i].Models = append([]string(nil), dyn.Models...)
		}
		if len(dyn.Modes) > 0 {
			updated[i].Modes = append([]string(nil), dyn.Modes...)
		}
	}
	c.mu.Lock()
	c.options = updated
	c.mu.Unlock()
}
