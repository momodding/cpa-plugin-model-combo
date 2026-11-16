package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const entitlementUnknown = "unknown"

// errAccountCatalogUnavailable marks a catalog that could not be read; callers must fail closed.
var errAccountCatalogUnavailable = errors.New("credential catalog unavailable")

// authCatalogHost is the optional host capability behind the account catalog. Both calls need a
// valid host callback context.
type authCatalogHost interface {
	AuthModels() ([]pluginapi.HostAuthModels, error)
	AuthList() ([]pluginapi.HostAuthFileEntry, error)
}

// accountCredential is the safe browser/runtime view of one credential; it never carries tokens,
// paths, metadata or the complete SDK auth object.
type accountCredential struct {
	ID        string         `json:"id"`
	AuthIndex string         `json:"auth_index"`
	Provider  string         `json:"provider"`
	Label     string         `json:"label"`
	Disabled  bool           `json:"disabled"`
	Models    []accountModel `json:"models"`
}

// accountModel is registry membership only. Entitlement stays unknown because stock sources
// cannot verify model access.
type accountModel struct {
	ID          string `json:"id"`
	Entitlement string `json:"entitlement"`
}

// accountCatalog joins host.auth.models with host.auth.list and refreshes the label snapshot used by
// usage ingestion. Any host failure is returned wrapped in errAccountCatalogUnavailable.
func (p *modelRouterPlugin) accountCatalog(host modelHost) ([]accountCredential, error) {
	source, ok := host.(authCatalogHost)
	if !ok {
		return nil, fmt.Errorf("%w: host callback is unavailable", errAccountCatalogUnavailable)
	}
	registered, err := source.AuthModels()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errAccountCatalogUnavailable, err)
	}
	entries, err := source.AuthList()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errAccountCatalogUnavailable, err)
	}
	if p != nil {
		p.credentials.replace(entries)
	}
	byID := make(map[string]pluginapi.HostAuthFileEntry, len(entries))
	for _, entry := range entries {
		byID[strings.TrimSpace(entry.ID)] = entry
	}
	aliases := map[string]struct{}{}
	if p != nil {
		for _, route := range p.config.Routes {
			aliases[routeKey(route.Alias)] = struct{}{}
		}
	}
	credentials := make([]accountCredential, 0, len(registered))
	seen := make(map[string]struct{}, len(registered))
	for _, item := range registered {
		id := strings.TrimSpace(item.ID)
		provider := strings.TrimSpace(item.Provider)
		if _, duplicate := seen[id]; id == "" || duplicate || provider == pluginID {
			continue
		}
		seen[id] = struct{}{}
		entry := byID[id]
		credential := accountCredential{
			ID:        id,
			AuthIndex: strings.TrimSpace(item.AuthIndex),
			Provider:  provider,
			Label:     safeCredentialLabel(entry.Label, entry.Email),
			Disabled:  entry.Disabled,
			Models:    make([]accountModel, 0, len(item.Models)),
		}
		modelSeen := make(map[string]struct{}, len(item.Models))
		for _, model := range item.Models {
			model = strings.TrimSpace(model)
			if _, self := aliases[routeKey(model)]; self || model == "" {
				continue
			}
			if _, duplicate := modelSeen[model]; duplicate {
				continue
			}
			modelSeen[model] = struct{}{}
			credential.Models = append(credential.Models, accountModel{ID: model, Entitlement: entitlementUnknown})
		}
		credentials = append(credentials, credential)
	}
	sort.SliceStable(credentials, func(i, j int) bool { return credentials[i].ID < credentials[j].ID })
	return credentials, nil
}

// supportsModel compares registry IDs exactly, accepting the target with its thinking suffix removed.
func (c accountCredential) supportsModel(target string) bool {
	base, _, _ := splitThinkingSuffix(strings.TrimSpace(target))
	for _, model := range c.Models {
		if model.ID == target || model.ID == base {
			return true
		}
	}
	return false
}
