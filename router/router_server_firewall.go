package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/pterodactyl/wings/router/middleware"
)

// postServerFirewallAdd handles a request from the Panel to add a firewall rule (ban an IP).
func postServerFirewallAdd(c *gin.Context) {
	s := ExtractServer(c)

	var data struct {
		IP         string `json:"ip" binding:"required"`
		Reason     string `json:"reason"`
		ServerIP   string `json:"server_ip" binding:"required"`
		ServerPort int    `json:"server_port" binding:"required"`
	}

	if err := c.ShouldBindJSON(&data); err != nil {
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}

	if err := s.AddFirewallRule(data.IP, data.ServerIP, data.ServerPort); err != nil {
		middleware.CaptureAndAbort(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// deleteServerFirewallRule handles a request to remove a single IP ban.
func deleteServerFirewallRule(c *gin.Context) {
	s := ExtractServer(c)

	var data struct {
		IP         string `json:"ip" binding:"required"`
		ServerIP   string `json:"server_ip" binding:"required"`
		ServerPort int    `json:"server_port" binding:"required"`
	}

	if err := c.ShouldBindJSON(&data); err != nil {
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}

	if err := s.RemoveFirewallRule(data.IP); err != nil {
		middleware.CaptureAndAbort(c, err)
		return
	}

	c.Status(http.StatusNoContent)
}

// deleteServerFirewallFlush removes ALL firewall rules for a server.
// Called during server deletion to ensure no orphaned iptables rules remain.
func deleteServerFirewallFlush(c *gin.Context) {
	s := ExtractServer(c)

	// This is best-effort — we don't fail the request if iptables cleanup has issues.
	s.FlushFirewallRules()

	c.Status(http.StatusNoContent)
}
