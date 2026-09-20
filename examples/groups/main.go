// This example uses public, in-memory test credentials to demonstrate group
// middleware and request-local values. Applications supply real credential checks.
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/starstack/gola"
)

type identityKey struct{}
type identity struct{ UserID, TenantID string }

func newRouter() *gola.Engine {
	r := gola.Default()
	r.GET("/healthz", func(c *gola.Context) { c.Status(http.StatusNoContent) })
	api := r.Group("/api")
	v1 := api.Group("/v1", demoAuthentication)
	v1.GET("/me", func(c *gola.Context) {
		user := c.Request.Context().Value(identityKey{}).(identity)
		if err := c.JSON(http.StatusOK, user); err != nil {
			c.Fail(err)
		}
	})
	return r
}

func demoAuthentication(c *gola.Context) {
	// Fixed test data makes the allow/deny paths reproducible; replace the whole
	// middleware with application-owned credential and membership verification.
	if c.GetHeader("Authorization") != "Bearer demo-only-token" {
		if err := c.AbortWithStatusJSON(http.StatusUnauthorized, map[string]string{"error": "authentication required"}); err != nil {
			c.Error(err)
		}
		return // Abort does not return from the current Go function.
	}
	verified := identity{UserID: "demo-danny", TenantID: "demo-stardata"}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), identityKey{}, verified))
	c.Next()
}

func main() {
	if err := newRouter().Run("127.0.0.1:8080"); err != nil {
		log.Fatal(err)
	}
}
