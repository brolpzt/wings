package server

import (
	"context"

	"github.com/pterodactyl/wings/remote"
)

type AddonExecuteRequest struct {
	Script         string `json:"script"`
	ContainerImage string `json:"container_image"`
}

// ExecuteAddon executes a custom installation script (addon) for the server.
func (s *Server) ExecuteAddon(ctx context.Context, req AddonExecuteRequest) error {
	// Create a temporary installation script struct
	script := remote.InstallationScript{
		ContainerImage: req.ContainerImage,
		Entrypoint:     "ash", // Use ash/sh as entrypoint for install scripts
		Script:         req.Script,
	}

	// If entrypoint is not specified, we might want to detect it, but "ash" is standard for alpine.
	// Pterodactyl usually uses /bin/bash or /bin/ash.

	p, err := NewInstallationProcess(s, &script)
	if err != nil {
		return err
	}

	s.Log().Info("beginning addon execution process for server")
	if err := p.Run(); err != nil {
		return err
	}

	s.Log().Info("completed addon execution process for server")
	return nil
}
