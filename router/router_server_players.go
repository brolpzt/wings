package router

import (
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

		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}
