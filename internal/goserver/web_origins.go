package goserver

import (
	"fmt"
	"net"
	"sort"

	"github.com/SurTeam/Water/internal/goconfig"
)

// Wildcard listeners only accept origins for addresses owned by this server.
// Resolve this once on startup, outside both HTTP handling and the GUI frame.
func webAccessOrigins(cfg goconfig.WebConfig) (string, map[string]struct{}, error) {
	cfg = cfg.Normalized()
	origins := map[string]struct{}{}
	origin := cfg.Origin()
	ip := net.ParseIP(cfg.ListenAddress)
	if ip == nil || !ip.IsUnspecified() {
		origins[origin] = struct{}{}
		return origin, origins, nil
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", nil, err
	}
	hosts := []string{}
	pointToPoint := map[string]bool{}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			return "", nil, err
		}
		for _, address := range addresses {
			candidate, _, err := net.ParseCIDR(address.String())
			if err != nil {
				continue
			}
			if candidate.IsUnspecified() || candidate.IsMulticast() || candidate.IsLinkLocalUnicast() {
				continue
			}
			if ip.To4() != nil && candidate.To4() == nil {
				continue
			}
			hosts = append(hosts, candidate.String())
			pointToPoint[candidate.String()] = iface.Flags&net.FlagPointToPoint != 0
			if len(hosts) > 256 {
				return "", nil, fmt.Errorf("too many HTTP access addresses")
			}
		}
	}
	// Keep the system's interface order within each class: lexical IP sorting
	// can prefer a VM bridge, and the default route can belong to a proxy tunnel.
	sort.SliceStable(hosts, func(i, j int) bool {
		rank := func(host string) int {
			v := net.ParseIP(host)
			if v.IsLoopback() {
				return 3
			}
			if v.To4() == nil {
				return 2
			}
			if pointToPoint[host] {
				return 1
			}
			return 0
		}
		return rank(hosts[i]) < rank(hosts[j])
	})
	if len(hosts) == 0 {
		return "", nil, fmt.Errorf("HTTP wildcard listener has no usable local address")
	}
	for _, host := range append(hosts, "localhost") {
		address := cfg
		address.ListenAddress = host
		origins[address.Origin()] = struct{}{}
	}
	access := cfg
	access.ListenAddress = hosts[0]
	return access.Origin(), origins, nil
}

func (w *webService) originLocked() string {
	if w.publicOrigin != "" {
		return w.publicOrigin
	}
	if ip := net.ParseIP(w.cfg.ListenAddress); ip != nil && ip.IsUnspecified() {
		return ""
	}
	return w.cfg.Origin()
}
