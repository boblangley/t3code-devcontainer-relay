package t3relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	dockerclient "github.com/moby/moby/client"
	"go.uber.org/zap"
)

// sanitizeRe matches characters not allowed in DNS labels.
var sanitizeRe = regexp.MustCompile(`[^a-z0-9-]+`)

// sanitizeName converts a container name to a valid DNS label segment.
func sanitizeName(name string) string {
	// strip leading /
	name = strings.TrimPrefix(name, "/")
	name = strings.ToLower(name)
	name = sanitizeRe.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	return name
}

// probeResult is the parsed response from GET /.well-known/t3/environment.
type probeResult struct {
	EnvironmentID string          `json:"environmentId"`
	Label         string          `json:"label"`
	Platform      json.RawMessage `json:"platform"`
	ServerVersion string          `json:"serverVersion"`
}

// probeEnvironment calls GET http://<ip>:<port>/.well-known/t3/environment
// with X-Relay-Secret header. Returns the raw body and parsed result.
func probeEnvironment(ip string, port int, secret []byte, timeout time.Duration) (string, *probeResult, error) {
	url := fmt.Sprintf("http://%s:%d/.well-known/t3/environment", ip, port)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", nil, err
	}
	if len(secret) > 0 {
		req.Header.Set("X-Relay-Secret", string(secret))
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", nil, err
	}
	var pr probeResult
	if err := json.Unmarshal(body, &pr); err != nil {
		return string(body), nil, nil
	}
	return string(body), &pr, nil
}

// runDiscovery runs the Docker discovery loop until ctx is canceled.
// It reconciles every 30 seconds. Event-driven reconcile is a future
// improvement; a 30s poll is sufficient for the initial implementation.
func runDiscovery(ctx context.Context, app *RelayApp) {
	reconcile(ctx, app)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile(ctx, app)
		}
	}
}

// reconcile performs one full discovery pass.
func reconcile(ctx context.Context, app *RelayApp) {
	logger := app.logger.Named("discovery")

	filters := make(dockerclient.Filters).Add("label", "devcontainer.id")
	result, err := app.docker.ContainerList(ctx, dockerclient.ContainerListOptions{
		Filters: filters,
	})
	if err != nil {
		if ctx.Err() == nil {
			logger.Error("ContainerList failed", zap.Error(err))
		}
		return
	}

	seenContainerIDs := make(map[string]bool)

	// Track hostnames assigned during this reconcile to detect collisions.
	// Maps sanitized-name → containerID that owns it.
	hostnameOwner := make(map[string]string)

	for _, c := range result.Items {
		devcontainerID := c.Labels["devcontainer.id"]
		if devcontainerID == "" {
			continue
		}

		seenContainerIDs[c.ID] = true

		// derive hostname base from label override or container name
		var baseName string
		if labelHost, ok := c.Labels["t3relay.host"]; ok && labelHost != "" {
			baseName = sanitizeName(labelHost)
		} else {
			// use first name (strip leading /)
			for _, n := range c.Names {
				baseName = sanitizeName(n)
				if baseName != "" {
					break
				}
			}
		}
		if baseName == "" {
			baseName = sanitizeName(c.ID[:12])
		}

		// collision detection per decision 0004
		hostName := baseName
		if ownerID, exists := hostnameOwner[baseName]; exists && ownerID != c.ID {
			// This container collides — suffix with short container ID
			hostName = fmt.Sprintf("%s-%s", baseName, c.ID[:6])
			logger.Warn("hostname collision",
				zap.String("base_name", baseName),
				zap.String("winner_container_id", ownerID),
				zap.String("loser_container_id", c.ID),
				zap.String("assigned_host", hostName),
			)
		} else {
			hostnameOwner[baseName] = c.ID
		}

		fullHostname := app.PublishedHostname(hostName)

		// find IP: prefer network containing "dev-ingress"
		ip := resolveContainerIP(app.docker, ctx, c.ID, logger)
		if ip == "" {
			logger.Warn("no IP found for container, skipping", zap.String("container_id", c.ID))
			continue
		}

		// probe
		rawBody, _, probeErr := probeEnvironment(ip, app.ProbePort, app.sharedSecret, 3*time.Second)
		status := "running"
		if probeErr != nil {
			status = "unreachable"
		}

		now := time.Now().Unix()
		env := Environment{
			ID:          devcontainerID,
			ContainerID: c.ID,
			Name:        hostName,
			Hostname:    fullHostname,
			IP:          ip,
			Port:        app.ProbePort,
			Status:      status,
			ProbeJSON:   rawBody,
			FirstSeen:   now,
			LastSeen:    now,
		}

		if err := app.store.Upsert(env); err != nil {
			logger.Error("store upsert failed", zap.Error(err), zap.String("id", devcontainerID))
		}
	}

	// Mark containers that disappeared as stopped.
	existing := app.store.List()
	for _, e := range existing {
		if e.Status == "stopped" {
			continue
		}
		if !seenContainerIDs[e.ContainerID] {
			if err := app.store.MarkStopped(e.ContainerID); err != nil {
				logger.Error("mark stopped failed", zap.Error(err), zap.String("container_id", e.ContainerID))
			}
		}
	}
}

// resolveContainerIP inspects a container and returns its IP address,
// preferring the network whose name contains "dev-ingress".
func resolveContainerIP(cli *dockerclient.Client, ctx context.Context, containerID string, logger *zap.Logger) string {
	insp, err := cli.ContainerInspect(ctx, containerID, dockerclient.ContainerInspectOptions{})
	if err != nil {
		logger.Error("ContainerInspect failed", zap.Error(err), zap.String("container_id", containerID))
		return ""
	}
	if insp.Container.NetworkSettings == nil {
		return ""
	}
	// prefer dev-ingress network
	for netName, ep := range insp.Container.NetworkSettings.Networks {
		if strings.Contains(netName, "dev-ingress") {
			if ep.IPAddress.IsValid() {
				return ep.IPAddress.String()
			}
		}
	}
	// fall back to first non-empty IP
	for _, ep := range insp.Container.NetworkSettings.Networks {
		if ep.IPAddress.IsValid() {
			return ep.IPAddress.String()
		}
	}
	return ""
}
