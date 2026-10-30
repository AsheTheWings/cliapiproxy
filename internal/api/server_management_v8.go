package api

import (
	"github.com/AsheTheWings/cliapiproxy/internal/api/handlers/management"
	"github.com/gin-gonic/gin"
)

// registerManagementV8Routes defines the management API: a read-only view of
// the configuration, the credentials in the auth directory and the OAuth
// flows that create them, and the model catalog. The server's inputs own its
// configuration, so no route writes it.
func (s *Server) registerManagementV8Routes() {
	const prefix = "/v8/management"
	s.engine.GET(prefix+"/oauth/callback", s.managementAvailabilityMiddleware(), s.mgmt.GetOAuthCallbackV8)
	s.engine.POST(prefix+"/oauth/callback", s.managementAvailabilityMiddleware(), s.mgmt.PostOAuthCallbackV8)

	v8 := s.engine.Group(prefix)
	v8.Use(s.managementAvailabilityMiddleware(), s.mgmt.Middleware(), func(c *gin.Context) {
		c.Set(management.ConfigV8ContextKey, true)
	})
	v8.GET("/config", s.mgmt.ConfigV8Effective)
	v8.GET("/config/*path", s.mgmt.ConfigV8Effective)

	v8.POST("/routing/cooldown/reset", s.mgmt.ResetQuota)
	v8.GET("/routing/model-definitions/:channel", s.mgmt.GetStaticModelDefinitions)
	v8.POST("/routing/model-definitions/refresh", s.mgmt.RefreshModelDefinitions)
	v8.POST("/models/refresh", s.mgmt.RefreshModelDefinitions)

	v8.GET("/credentials", s.mgmt.ListAuthFiles)
	v8.POST("/credentials", s.mgmt.UploadAuthFileV8)
	v8.DELETE("/credentials", s.mgmt.DeleteAuthFileV8)
	v8.GET("/credentials/models", s.mgmt.GetAuthFileModels)
	v8.GET("/credentials/download", s.mgmt.DownloadAuthFile)
	v8.PATCH("/credentials/status", s.mgmt.PatchAuthFileStatus)
	v8.PATCH("/credentials/fields", s.mgmt.PatchAuthFileFields)
	v8.POST("/credentials/refresh", s.mgmt.RefreshAuthFiles)
	v8.POST("/oauth/import", s.mgmt.ImportOAuthV8)
	v8.GET("/oauth/auth-url", s.mgmt.StartOAuthV8)
	v8.POST("/oauth/auth-url", s.mgmt.StartOAuthV8)
	v8.POST("/oauth/siwc/bind", s.mgmt.StartSIWCBind)
	v8.GET("/oauth/siwc/status", s.mgmt.GetSIWCStatus)
	v8.GET("/credentials/siwc/status", s.mgmt.GetSIWCStatus)
	v8.GET("/oauth/status", s.mgmt.GetAuthStatus)
	v8.DELETE("/oauth/session", s.mgmt.CancelAuthSession)
}
