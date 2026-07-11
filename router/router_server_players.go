package router

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/pterodactyl/wings/consoleplayers"
)

// getServerPlayers sends `status` to the server console and parses player data from recent logs.
func getServerPlayers(c *gin.Context) {
	s := ExtractServer(c)

	result, err := consoleplayers.Fetch(c.Request.Context(), s.Environment)
	if err != nil {
		if err.Error() == "server is not running" {
			c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{
				"error": "Cannot query players on a stopped server instance.",
			})
			return
		}

		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			c.AbortWithStatusJSON(http.StatusGatewayTimeout, gin.H{
				"error": "Timed out while querying players from the server console.",
			})
			return
		}

		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}
