package router

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/pterodactyl/wings/environment/docker"
	"github.com/pterodactyl/wings/gamequery"
	"github.com/pterodactyl/wings/server"
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
	// Allocations are typically bound to the public node IP (not 0.0.0.0). Querying
	// that address from Wings on the same host fails without hairpin NAT, and
	// 127.0.0.1 also fails because the port is not published there. Prefer the
	// container bridge IP so local UDP queries reach the game process.
	host := resolveQueryHost(c.Request.Context(), s, alloc.Ip)
	// EOS (Evrima / Dragonwilds) matchmaking indexes sessions by the public IP
	// advertised as ADDRESS_s — never the Docker bridge address.
	publicHost := alloc.Ip
	if publicHost == "" || publicHost == "0.0.0.0" {
		publicHost = host
	}
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
		GameType:   gameType,
		Host:       host,
		PublicHost: publicHost,
		Port:       port,
		QueryPort:  queryPort,
	})
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, result)
}

func resolveQueryHost(ctx context.Context, s *server.Server, allocationIP string) string {
	if env, ok := s.Environment.(*docker.Environment); ok && env != nil {
		if insp, err := env.ContainerInspect(ctx); err == nil {
			for _, network := range insp.NetworkSettings.Networks {
				if network != nil && network.IPAddress != "" {
					return network.IPAddress
				}
			}
		}
	}

	switch allocationIP {
	case "", "0.0.0.0":
		return "127.0.0.1"
	default:
		return allocationIP
	}
}
