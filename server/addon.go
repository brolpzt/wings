package server

import (
	"context"
	"strings"

	"github.com/pterodactyl/wings/remote"
)

type AddonExecuteRequest struct {
	Script         string `json:"script"`
	ContainerImage string `json:"container_image"`
	Entrypoint     string `json:"entrypoint"`
}

// ExecuteAddon executes a custom installation script (addon) for the server.
func (s *Server) ExecuteAddon(ctx context.Context, req AddonExecuteRequest) error {
	script := remote.InstallationScript{
		ContainerImage: req.ContainerImage,
		Entrypoint:     resolveAddonEntrypoint(req.Entrypoint, req.ContainerImage),
		Script:         req.Script,
	}

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

// resolveAddonEntrypoint picks a shell that exists in the installer image.
// Debian/Ubuntu images ship bash; Alpine images ship ash.
func resolveAddonEntrypoint(requested, image string) string {
	if requested = strings.TrimSpace(requested); requested != "" {
		return requested
	}

	lower := strings.ToLower(image)
	if strings.Contains(lower, "alpine") {
		return "ash"
	}

	return "bash"
}
