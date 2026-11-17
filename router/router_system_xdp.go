package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/pterodactyl/wings/router/middleware"
	"github.com/pterodactyl/wings/xdp"
)

type xdpSyncRequest struct {
	// Port (as string) -> profile. The Panel owns the whole map.
	Ports  map[string]string `json:"ports"`
	DryRun bool              `json:"dry_run"`
}

// getSystemXdp returns the ports the node's XDP game firewall protects.
func getSystemXdp(c *gin.Context) {
	if !xdp.Available() {
		c.AbortWithStatusJSON(http.StatusNotImplemented, gin.H{"error": "game_firewall is not installed on this node."})
		return
	}
	ports, err := xdp.List()
	if err != nil {
		middleware.CaptureAndAbort(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ports": ports})
}

// putSystemXdp makes the node's XDP port map equal to the one sent by the Panel.
func putSystemXdp(c *gin.Context) {
	var body xdpSyncRequest
	if err := c.BindJSON(&body); err != nil {
		return
	}
	if !xdp.Available() {
		c.AbortWithStatusJSON(http.StatusNotImplemented, gin.H{"error": "game_firewall is not installed on this node."})
		return
	}
	desired, err := xdp.Normalize(body.Ports)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	res, err := xdp.Sync(desired, body.DryRun)
	if err != nil {
		middleware.CaptureAndAbort(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}
