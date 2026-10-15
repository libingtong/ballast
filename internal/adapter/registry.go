package adapter

import (
	"github.com/libingtong/ballast/internal/adapter/claudecode"
	"github.com/libingtong/ballast/internal/adapter/codex"
	"github.com/libingtong/ballast/internal/adapter/cursor"
	"github.com/libingtong/ballast/internal/adapter/hermes"
	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
)

// Registry implements ports.AdapterRegistry.
type Registry struct {
	list []ports.Adapter
}

var _ ports.AdapterRegistry = (*Registry)(nil)

// NewRegistry returns a registry over the given adapters (order preserved).
func NewRegistry(as ...ports.Adapter) *Registry { return &Registry{list: as} }

// Default returns the registry of the four built-in adapters.
func Default() *Registry {
	return NewRegistry(claudecode.New(), codex.New(), cursor.New(), hermes.New())
}

// Get implements ports.AdapterRegistry.
func (r *Registry) Get(id model.ClientID) (ports.Adapter, bool) {
	for _, a := range r.list {
		if a.ID() == id {
			return a, true
		}
	}
	return nil, false
}

// All implements ports.AdapterRegistry.
func (r *Registry) All() []ports.Adapter { return r.list }
