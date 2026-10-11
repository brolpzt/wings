package router

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/pterodactyl/wings/nftables"
	"github.com/pterodactyl/wings/router/middleware"
)

type nftablesSyncRequest struct {
	Rules []nftables.Rule `json:"rules"`
}

// putSystemNftables replaces the managed inet ptero / ptero_ports chain.
func putSystemNftables(c *gin.Context) {
	var body nftablesSyncRequest
	if err := c.BindJSON(&body); err != nil {
		return
	}

	if body.Rules == nil {
		body.Rules = []nftables.Rule{}
	}

	if err := nftables.Sync(body.Rules); err != nil {
		middleware.CaptureAndAbort(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"synced": len(body.Rules),
	})
}
