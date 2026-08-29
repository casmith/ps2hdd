// Package provider supplies artwork from a remote database or a local mirror.
package provider

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/casmith/ps2hdd/internal/model"
)

// Provider looks up and fetches artwork for a game.
//
// The interface is deliberately narrow so ps2hdd is not tied to any one
// community database: those come and go, and a user with a local OPL Manager
// art dump should be able to use it without a code change.
type Provider interface {
	// Name identifies the provider in configuration and diagnostics.
	Name() string
	// Lookup reports which of the wanted asset types this provider can supply
	// for a game. It must not download anything.
	Lookup(ctx context.Context, game model.Game, want []model.AssetType) (model.AssetSet, error)
	// Fetch opens one asset returned by Lookup. The caller closes the reader.
	Fetch(ctx context.Context, a model.Asset) (io.ReadCloser, error)
	// Check reports whether the provider is reachable, for `ps2hdd doctor`.
	Check(ctx context.Context) error
}

// Registry maps provider names to constructors.
type Registry struct {
	factories map[string]func(Options) (Provider, error)
}

// Options configure a provider at construction time.
type Options struct {
	// Mirror is the local directory a "local" provider reads from.
	Mirror string
	// Templates overrides the URL templates of an HTTP provider. Keys are
	// asset type names (COV, BG, ...).
	Templates map[string]string
	// HTTP lets a caller supply a client with a different timeout or proxy.
	HTTP Doer
	// CacheDir is where downloads are cached.
	CacheDir string
}

// NewRegistry returns a registry with the built-in providers.
func NewRegistry() *Registry {
	r := &Registry{factories: map[string]func(Options) (Provider, error){}}
	r.Register("ps2-covers", newPS2Covers)
	r.Register("http", newHTTPTemplate)
	r.Register("local", newLocal)
	return r
}

// Register adds a provider constructor.
func (r *Registry) Register(name string, f func(Options) (Provider, error)) {
	r.factories[name] = f
}

// Names lists the registered providers.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.factories))
	for n := range r.factories {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// New builds a provider by name.
func (r *Registry) New(name string, opts Options) (Provider, error) {
	f, ok := r.factories[name]
	if !ok {
		return nil, fmt.Errorf("unknown artwork provider %q (available: %s)", name, strings.Join(r.Names(), ", "))
	}
	return f(opts)
}

// Chain tries several providers in order, taking the first that has each
// asset. It is what makes "a local mirror, falling back to the network"
// expressible without either provider knowing about the other.
type Chain struct {
	Providers []Provider
}

// Name implements Provider.
func (c Chain) Name() string {
	names := make([]string, 0, len(c.Providers))
	for _, p := range c.Providers {
		names = append(names, p.Name())
	}
	return strings.Join(names, "+")
}

// Lookup implements Provider, preferring earlier providers for each type.
func (c Chain) Lookup(ctx context.Context, game model.Game, want []model.AssetType) (model.AssetSet, error) {
	var out model.AssetSet
	remaining := append([]model.AssetType(nil), want...)
	var lastErr error
	for _, p := range c.Providers {
		if len(remaining) == 0 {
			break
		}
		set, err := p.Lookup(ctx, game, remaining)
		if err != nil {
			lastErr = err
			continue
		}
		found := map[model.AssetType]bool{}
		for _, a := range set.Assets {
			out.Assets = append(out.Assets, a)
			found[a.Type] = true
		}
		var still []model.AssetType
		for _, t := range remaining {
			if !found[t] {
				still = append(still, t)
			}
		}
		remaining = still
	}
	if len(out.Assets) == 0 && lastErr != nil {
		return out, lastErr
	}
	return out, nil
}

// Fetch implements Provider by routing to the provider that produced the asset.
func (c Chain) Fetch(ctx context.Context, a model.Asset) (io.ReadCloser, error) {
	var lastErr error
	for _, p := range c.Providers {
		rc, err := p.Fetch(ctx, a)
		if err == nil {
			return rc, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// Check implements Provider, succeeding when any member is reachable.
func (c Chain) Check(ctx context.Context) error {
	var lastErr error
	for _, p := range c.Providers {
		if err := p.Check(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}
