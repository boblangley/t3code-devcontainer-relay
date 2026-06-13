package t3relay

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	dockerclient "github.com/moby/moby/client"
	"go.uber.org/zap"
)

func discoverServedZones(ctx context.Context, cli *dockerclient.Client, logger *zap.Logger) ([]string, error) {
	containerID, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("discover served zones: hostname: %w", err)
	}

	inspected, err := cli.ContainerInspect(ctx, containerID, dockerclient.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("discover served zones: inspect self %q: %w", containerID, err)
	}

	zones := wildcardZonesFromLabels(inspected.Container.Config.Labels)
	if len(zones) == 0 {
		logger.Warn("no wildcard Caddy labels found on relay container; falling back to static domain settings")
	}
	return zones, nil
}

func wildcardZonesFromLabels(labels map[string]string) []string {
	seen := make(map[string]struct{})
	for key, value := range labels {
		if !isCaddySiteLabel(key) {
			continue
		}
		for _, host := range strings.Split(value, ",") {
			host = normalizeHost(host)
			if zone, ok := wildcardZoneFromHost(host); ok {
				seen[zone] = struct{}{}
			}
		}
	}

	zones := make([]string, 0, len(seen))
	for zone := range seen {
		zones = append(zones, zone)
	}
	sort.Strings(zones)
	sort.SliceStable(zones, func(i, j int) bool {
		return len(zones[i]) > len(zones[j])
	})
	return zones
}

func isCaddySiteLabel(key string) bool {
	if key == "caddy" {
		return true
	}
	if !strings.HasPrefix(key, "caddy_") {
		return false
	}
	suffix := strings.TrimPrefix(key, "caddy_")
	if suffix == "" {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func wildcardZoneFromHost(host string) (string, bool) {
	if !strings.HasPrefix(host, "*.") {
		return "", false
	}
	zone := strings.TrimPrefix(host, "*.")
	parts := strings.Split(zone, ".")
	if len(parts) < 3 || parts[0] != "t3" {
		return "", false
	}
	return zone, true
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimSuffix(host, ".")
	return host
}

func parseServedHost(host string, zones []string) (name, zone string, ok bool) {
	host = normalizeHost(host)
	if host == "" {
		return "", "", false
	}
	for _, zone = range zones {
		suffix := "." + zone
		if !strings.HasSuffix(host, suffix) {
			continue
		}
		name = strings.TrimSuffix(host, suffix)
		if name == "" || strings.Contains(name, ".") {
			return "", "", false
		}
		return name, zone, true
	}
	return "", "", false
}

func primaryZone(zones []string, fallbackDomainSuffix string, fallbackRelayHost string) string {
	if len(zones) > 0 {
		return zones[0]
	}
	fallbackDomainSuffix = normalizeHost(fallbackDomainSuffix)
	if fallbackDomainSuffix != "" {
		return fallbackDomainSuffix
	}
	fallbackRelayHost = normalizeHost(fallbackRelayHost)
	if strings.HasPrefix(fallbackRelayHost, "relay.") {
		return strings.TrimPrefix(fallbackRelayHost, "relay.")
	}
	return ""
}
