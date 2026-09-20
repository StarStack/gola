package main

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/starstack/gola"
)

type createUser struct {
	Name string `json:"name"`
}
type validator struct{}

func (validator) Validate(value any) error {
	if user, ok := value.(*createUser); ok && strings.TrimSpace(user.Name) == "" {
		return errors.New("name is required")
	}
	return nil
}

func newRouter() *gola.Engine {
	r := gola.Default(gola.WithBodyLimit(1<<20), gola.WithValidator(validator{}), gola.WithDisallowUnknownFields(true))
	r.POST("/users", func(c *gola.Context) {
		var input createUser
		if err := c.ShouldBindJSON(&input); err != nil {
			status, message := http.StatusBadRequest, "invalid JSON input"
			switch {
			case errors.Is(err, gola.ErrBodyTooLarge):
				status, message = http.StatusRequestEntityTooLarge, "request body too large"
			case errors.Is(err, gola.ErrUnsupportedMediaType):
				status, message = http.StatusUnsupportedMediaType, "JSON content type required"
			case errors.Is(err, gola.ErrValidation):
				message = "name is required"
			}
			c.Error(err)
			if err := c.AbortWithStatusJSON(status, map[string]string{"error": message}); err != nil {
				c.Error(err)
			}
			return
		}
		if err := c.JSON(http.StatusCreated, input); err != nil {
			c.Fail(err)
		}
	})
	return r
}

func main() {
	if err := newRouter().Run("127.0.0.1:8080"); err != nil {
		log.Fatal(err)
	}
}
