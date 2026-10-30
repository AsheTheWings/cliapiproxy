package auth

import "strings"

// IsConfigAPIKeyAuth reports whether the auth entry is synthesized from config *-api-key lists.
func IsConfigAPIKeyAuth(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if auth.AuthKind() != AuthKindAPIKey {
		return false
	}
	return auth.AuthSourceKind() == AuthSourceConfig
}

// IsAccountCatalogAuth reports whether a config API-key credential serves the
// model catalog of its key's account (Command Code). Unlike other API-key
// credentials it is refreshed: each refresh reloads the catalog into its
// metadata and sets the metadata expiry to the next reload.
func IsAccountCatalogAuth(auth *Auth) bool {
	return IsConfigAPIKeyAuth(auth) && strings.EqualFold(authAttribute(auth, AttributeAccountCatalog), "true")
}
