package commandcode

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

const (
	// Provider is the provider key of Command Code credentials and executors.
	Provider = "commandcode"
	// ModelPrefix starts the public id of every Command Code model.
	ModelPrefix = "commandcode/"
	// CatalogMetadataKey holds a credential's catalog in its metadata.
	CatalogMetadataKey = "commandcode_catalog"
)

// plans lists the plan ids from the lowest to the highest rank.
var plans = []string{
	"individual-go", "individual-go-v1", "individual-goat", "individual-pro", "individual-pro-v1",
	"individual-provider", "individual-max", "individual-ultra", "teams-pro",
}

const (
	categoryOpenSource = "opensource"
	categoryPremium    = "premium"
)

// planCategories are the model categories each plan includes.
var planCategories = map[string][]string{
	"individual-go":       {categoryOpenSource},
	"individual-go-v1":    {categoryOpenSource},
	"individual-goat":     {categoryOpenSource},
	"individual-pro":      {categoryPremium, categoryOpenSource},
	"individual-pro-v1":   {categoryPremium, categoryOpenSource},
	"individual-provider": {categoryPremium, categoryOpenSource},
	"individual-max":      {categoryPremium, categoryOpenSource},
	"individual-ultra":    {categoryPremium, categoryOpenSource},
	"teams-pro":           {categoryPremium, categoryOpenSource},
}

// CatalogModel is one entry of the model catalog.
type CatalogModel struct {
	ID               string   `json:"id"`
	Name             string   `json:"name,omitempty"`
	Category         string   `json:"category,omitempty"`
	MinimumPlan      string   `json:"minimumPlan,omitempty"`
	ContextLength    int      `json:"context_length,omitempty"`
	MaxOutputTokens  int      `json:"maxOutputTokens,omitempty"`
	InputModalities  []string `json:"inputModalities,omitempty"`
	ReasoningEfforts []string `json:"reasoningEfforts,omitempty"`
}

// Catalog is the model catalog together with the account state it is
// filtered by.
type Catalog struct {
	Version string         `json:"catalog_version,omitempty"`
	Models  []CatalogModel `json:"models"`
	Account Account        `json:"account"`
}

// ParseCatalog reads the answer of the client view of the model route, which
// lists the models under data.
func ParseCatalog(data []byte) (*Catalog, error) {
	var reply struct {
		CatalogVersion string         `json:"catalogVersion"`
		Data           []CatalogModel `json:"data"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return nil, fmt.Errorf("commandcode: parse model catalog: %w", err)
	}
	models := reply.Data
	catalog := &Catalog{Version: reply.CatalogVersion}
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" {
			continue
		}
		if _, dup := seen[model.ID]; dup {
			continue
		}
		seen[model.ID] = struct{}{}
		catalog.Models = append(catalog.Models, model)
	}
	if len(catalog.Models) == 0 {
		return nil, errors.New("commandcode: model catalog is empty")
	}
	return catalog, nil
}

// Allows reports whether the account may use the model, by the rules the CLI
// applies before it sends a request: an account with purchased or free credits
// may use every model, and so may an account whose plan is unknown.
func (a Account) Allows(model CatalogModel) bool {
	if a.PurchasedCredits > 0 || a.FreeCredits > 0 || a.PlanID == "" {
		return true
	}
	categories, known := planCategories[a.PlanID]
	if !known {
		return true
	}
	if model.Category != categoryOpenSource && model.Category != categoryPremium {
		return true
	}
	inCategories := func() bool {
		for _, category := range categories {
			if category == model.Category {
				return true
			}
		}
		return false
	}
	if model.MinimumPlan == "" {
		return inCategories()
	}
	have, need := planRank(a.PlanID), planRank(model.MinimumPlan)
	if have >= 0 && need >= 0 {
		return have >= need
	}
	return inCategories()
}

func planRank(plan string) int {
	for rank, name := range plans {
		if name == plan {
			return rank
		}
	}
	return -1
}

// Available returns the models the account behind the catalog may use.
func (c *Catalog) Available() []CatalogModel {
	if c == nil {
		return nil
	}
	models := make([]CatalogModel, 0, len(c.Models))
	for _, model := range c.Models {
		if c.Account.Allows(model) {
			models = append(models, model)
		}
	}
	return models
}

// Model returns the entry with the given gateway model id.
func (c *Catalog) Model(modelID string) (CatalogModel, bool) {
	if c == nil {
		return CatalogModel{}, false
	}
	for _, model := range c.Models {
		if model.ID == modelID {
			return model, true
		}
	}
	return CatalogModel{}, false
}

// Metadata returns the form in which a credential's metadata stores the catalog.
func (c *Catalog) Metadata(fetchedAt time.Time) (map[string]any, error) {
	raw, err := json.Marshal(struct {
		FetchedAt string `json:"fetched_at"`
		*Catalog
	}{fetchedAt.UTC().Format(time.RFC3339), c})
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
	var catalog Catalog
	if json.Unmarshal(raw, &catalog) != nil || len(catalog.Models) == 0 {
		return nil, false
	}
	return &catalog, true
}

// Efforts are the reasoning efforts a model accepts, without "off".
func (m CatalogModel) Efforts() (levels []string, canDisable bool) {
	for _, effort := range m.ReasoningEfforts {
		switch effort = strings.TrimSpace(effort); effort {
		case "":
		case "off":
			canDisable = true
		default:
			levels = append(levels, effort)
		}
	}
	return levels, canDisable
}

// SupportsImages reports whether the model takes image input.
func (m CatalogModel) SupportsImages() bool {
	for _, kind := range m.InputModalities {
		if strings.EqualFold(strings.TrimSpace(kind), "image") {
			return true
		}
	}
	return false
}

// ModelInfo describes the model in the proxy's model registry.
func (m CatalogModel) ModelInfo() *registry.ModelInfo {
	name := m.Name
	if name == "" {
		name = m.ID
	}
	info := &registry.ModelInfo{
		ID:                  ModelPrefix + m.ID,
		Object:              "model",
		OwnedBy:             Provider,
		Type:                Provider,
		DisplayName:         name,
		ContextLength:       m.ContextLength,
		MaxCompletionTokens: m.MaxOutputTokens,
	}
	for _, kind := range m.InputModalities {
		if kind = strings.ToLower(strings.TrimSpace(kind)); kind != "" {
			info.SupportedInputModalities = append(info.SupportedInputModalities, kind)
		}
	}
	if levels, canDisable := m.Efforts(); len(levels) > 0 {
		info.Thinking = &registry.ThinkingSupport{Levels: levels, ZeroAllowed: canDisable}
	}
	return info
}

// ModelInfos describes the models the account may use.
func (c *Catalog) ModelInfos() []*registry.ModelInfo {
	available := c.Available()
	infos := make([]*registry.ModelInfo, 0, len(available))
	for _, model := range available {
		infos = append(infos, model.ModelInfo())
	}
	return infos
}

// UpstreamModel returns the model id the gateway knows for a public id.
func UpstreamModel(publicID string) string {
	return strings.TrimPrefix(strings.TrimSpace(publicID), ModelPrefix)
}
