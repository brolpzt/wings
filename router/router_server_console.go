package router

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/pterodactyl/wings/consolecommand"
)

type consoleQueryRequest struct {
	Command string `json:"command"`
}

// postServerConsoleQuery sends a command to the server console and returns recent log output.
func postServerConsoleQuery(c *gin.Context) {
	s := ExtractServer(c)

	var body consoleQueryRequest
	if err := c.BindJSON(&body); err != nil {
		return
	}

	result, err := consolecommand.Execute(c.Request.Context(), s.Environment, body.Command)
	if err != nil {
		if err.Error() == "server is not running" {
			c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{
				"error": "Cannot run console commands on a stopped server instance.",
			})
			return
		}

		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			c.AbortWithStatusJSON(http.StatusGatewayTimeout, gin.H{
				"error": "Timed out while reading the server console output.",
			})
			return
		}

		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}
