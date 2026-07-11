package router

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/pterodactyl/wings/gamequery"
)

// getServerQuery handles a request from the Panel to query the game server status.
// The query is executed locally on the Wings node so UDP does not need to leave the VPS.
func getServerQuery(c *gin.Context) {
	s := ExtractServer(c)

	gameType := c.Query("type")
	if gameType == "" {
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
			"error": "The game type query parameter is required.",
		})
		return
	}

	alloc := s.Config().Allocations.DefaultMapping
	host := alloc.Ip
	port := alloc.Port

	queryPort := port
	if value := c.Query("query_port"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 65535 {
			c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
				"error": "The query_port parameter must be a valid port between 1 and 65535.",
			})
			return
		}
		queryPort = parsed
	}

	result, err := gamequery.Query(c.Request.Context(), gamequery.Request{
		GameType:  gameType,
		Host:      host,
		Port:      port,
		QueryPort: queryPort,
	})
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}
