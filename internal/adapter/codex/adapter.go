package codex

import (
	"encoding/json"

	"github.com/libingtong/ballast/internal/model"
	"github.com/libingtong/ballast/internal/ports"
)

// Adapter implements ports.Adapter for model.ClientCodex.
type Adapter struct{}

var _ ports.Adapter = (*Adapter)(nil)

// New returns the adapter.
func New() *Adapter { return &Adapter{} }

// ID implements ports.Adapter.
func (*Adapter) ID() model.ClientID { return model.ClientCodex }

// Capabilities implements ports.Adapter.
func (*Adapter) Capabilities() model.CapabilitySet { return model.Capabilities(model.ClientCodex) }

// NativeEvents implements ports.Adapter.
func (*Adapter) NativeEvents() []model.NativeEvent { return model.NativeEventTable[model.ClientCodex] }

// ParseEvent implements ports.Adapter.
func (a *Adapter) ParseEvent(native string, stdin []byte) (*model.Event, error) {
	panic("unimplemented: codex.Adapter.ParseEvent")
}

// NormalizeTool implements ports.Adapter.
func (a *Adapter) NormalizeTool(rawName string, input json.RawMessage) (model.ToolCall, error) {
	panic("unimplemented: codex.Adapter.NormalizeTool")
}

// Render implements ports.Adapter.
func (a *Adapter) Render(ev *model.Event, d *model.Decision) ([]byte, error) {
	panic("unimplemented: codex.Adapter.Render")
}
