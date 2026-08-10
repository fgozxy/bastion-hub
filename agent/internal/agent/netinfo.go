package agent

import (
	"net"
	"os"
	"strings"
)

// netInfo returns the first global IPv4 and IPv6 of the host.
func netInfo() (ipv4, ipv6 string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				continue
			}
			if ip.To4() != nil {
				if ipv4 == "" {
					ipv4 = ip.String()
				}
			} else {
				if ipv6 == "" {
					// keep full address; the panel truncates for display
					ipv6 = ip.String()
				}
			}
		}
	}
	return
}

// hostName returns the system hostname.
func hostName() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(h)
}
