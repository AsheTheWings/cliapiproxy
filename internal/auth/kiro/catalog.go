package kiro

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/registry"
	"github.com/tidwall/gjson"
)

const (
	// Provider is the provider key of Kiro credentials and executors.
	Provider = "kiro"
	// ModelPrefix starts the public id of every Kiro model.
	ModelPrefix = "kiro/"
	// CatalogMetadataKey holds a credential's catalog in its metadata.
	CatalogMetadataKey = "kiro_catalog"
)

// Catalog is the model list of one account.
type Catalog struct {
	DefaultModel string
	Models       []CatalogModel
}

// CatalogModel is one entry of the model list.
type CatalogModel struct {
	ModelID             string   `json:"modelId"`
	ModelName           string   `json:"modelName,omitempty"`
	Description         string   `json:"description,omitempty"`
	RateMultiplier      float64  `json:"rateMultiplier,omitempty"`
	RateUnit            string   `json:"rateUnit,omitempty"`
	SupportedInputTypes []string `json:"supportedInputTypes,omitempty"`
	TokenLimits         struct {
		MaxInputTokens  int `json:"maxInputTokens,omitempty"`
		MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
	} `json:"tokenLimits"`
	PromptCaching struct {
		SupportsPromptCaching bool `json:"supportsPromptCaching"`
	} `json:"promptCaching"`
	// RequestFieldsSchema is the JSON Schema of the extra request fields the
	// model accepts.
	RequestFieldsSchema json.RawMessage `json:"additionalModelRequestFieldsSchema,omitempty"`
}

// ParseCatalog reads a ListAvailableModels response.
func ParseCatalog(data []byte) (*Catalog, error) {
	var reply struct {
		DefaultModel struct {
			ModelID string `json:"modelId"`
		} `json:"defaultModel"`
		Models []CatalogModel `json:"models"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return nil, fmt.Errorf("kiro: parse model catalog: %w", err)
	}
	catalog := &Catalog{DefaultModel: reply.DefaultModel.ModelID}
	seen := make(map[string]struct{}, len(reply.Models))
	for _, model := range reply.Models {
		model.ModelID = strings.TrimSpace(model.ModelID)
		if model.ModelID == "" {
			continue
		}
		if _, dup := seen[model.ModelID]; dup {
			continue
		}
		seen[model.ModelID] = struct{}{}
		catalog.Models = append(catalog.Models, model)
	}
	if len(catalog.Models) == 0 {
		return nil, errors.New("kiro: model catalog is empty")
	}
	return catalog, nil
}

// Model returns the entry with the given upstream model id.
func (c *Catalog) Model(modelID string) (CatalogModel, bool) {
	if c == nil {
		return CatalogModel{}, false
	}
	for _, model := range c.Models {
		if model.ModelID == modelID {
			return model, true
		}
	}
	return CatalogModel{}, false
}

// Metadata returns the form in which a credential file stores the catalog.
func (c *Catalog) Metadata(fetchedAt time.Time) (map[string]any, error) {
	raw, err := json.Marshal(struct {
		FetchedAt    string         `json:"fetched_at"`
		DefaultModel string         `json:"default_model,omitempty"`
		Models       []CatalogModel `json:"models"`
	}{fetchedAt.UTC().Format(time.RFC3339), c.DefaultModel, c.Models})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CatalogFromMetadata reads the catalog a credential's metadata holds.
func CatalogFromMetadata(metadata map[string]any) (*Catalog, bool) {
	stored, ok := metadata[CatalogMetadataKey]
	if !ok {
		return nil, false
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return nil, false
	}
	var shape struct {
		DefaultModel string         `json:"default_model"`
		Models       []CatalogModel `json:"models"`
	}
	if json.Unmarshal(raw, &shape) != nil || len(shape.Models) == 0 {
		return nil, false
	}
	return &Catalog{DefaultModel: shape.DefaultModel, Models: shape.Models}, true
}

// ReasoningStyle is how a model takes its reasoning setting.
type ReasoningStyle int

const (
	// ReasoningNone: the model accepts no reasoning field.
	ReasoningNone ReasoningStyle = iota
	// ReasoningEffort: {"effort": level}.
	ReasoningEffort
	// ReasoningAdaptive: {"type": "adaptive", "effort": level} or {"type": "disabled"}.
	ReasoningAdaptive
)

// Reasoning is the reasoning setting a model's schema allows.
type Reasoning struct {
	Style ReasoningStyle
	// Levels are the efforts the model accepts, without "none".
	Levels []string
	// Default is the effort the schema names as its default.
	Default string
	// CanDisable reports that the model can run with reasoning off.
	CanDisable bool
}

// Reasoning reads the reasoning setting from the model's request schema.
func (m CatalogModel) Reasoning() Reasoning {
	if len(m.RequestFieldsSchema) == 0 {
		return Reasoning{}
	}
	schema := gjson.ParseBytes(m.RequestFieldsSchema)
	var result Reasoning
	var effort gjson.Result
	switch {
	case schema.Get("properties.thinking").Exists():
		result.Style = ReasoningAdaptive
		effort = schema.Get("properties.output_config.properties.effort")
		for _, kind := range schema.Get("properties.thinking.properties.type.enum").Array() {
			if kind.String() == "disabled" {
				result.CanDisable = true
			}
		}
	case schema.Get("properties.reasoning.properties.effort").Exists():
		result.Style = ReasoningEffort
		effort = schema.Get("properties.reasoning.properties.effort")
	default:
		return Reasoning{}
	}
	for _, level := range effort.Get("enum").Array() {
		switch name := level.String(); name {
		case "":
		case "none":
			if result.Style == ReasoningEffort {
				result.CanDisable = true
			}
		default:
			result.Levels = append(result.Levels, name)
		}
	}
	result.Default = effort.Get("default").String()
	return result
}

// ModelInfo describes the model in the proxy's model registry.
func (m CatalogModel) ModelInfo() *registry.ModelInfo {
	name := m.ModelName
	if name == "" {
		name = m.ModelID
	}
	info := &registry.ModelInfo{
		ID:                  ModelPrefix + m.ModelID,
		Object:              "model",
		OwnedBy:             Provider,
		Type:                Provider,
		DisplayName:         name,
		Description:         m.Description,
		ContextLength:       m.TokenLimits.MaxInputTokens,
		MaxCompletionTokens: m.TokenLimits.MaxOutputTokens,
	}
	for _, kind := range m.SupportedInputTypes {
		if kind = strings.ToLower(strings.TrimSpace(kind)); kind != "" {
			info.SupportedInputModalities = append(info.SupportedInputModalities, kind)
		}
	}
	if reasoning := m.Reasoning(); reasoning.Style != ReasoningNone && len(reasoning.Levels) > 0 {
		info.Thinking = &registry.ThinkingSupport{
			Levels:      append([]string(nil), reasoning.Levels...),
			ZeroAllowed: reasoning.CanDisable,
		}
	}
	return info
}

// ModelInfos describes every model of the catalog.
func (c *Catalog) ModelInfos() []*registry.ModelInfo {
	if c == nil {
		return nil
	}
	infos := make([]*registry.ModelInfo, 0, len(c.Models))
	for _, model := range c.Models {
		infos = append(infos, model.ModelInfo())
	}
	return infos
}

// UpstreamModel returns the model id the Kiro service knows for a public id.
func UpstreamModel(publicID string) string {
	return strings.TrimPrefix(strings.TrimSpace(publicID), ModelPrefix)
}
