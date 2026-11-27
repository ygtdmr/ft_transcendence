package config

import (
	"net"

	sharedconfig "github.com/melmut-42/ft_transcendence/backend/shared/config"
)

// InternalAddress returns the private listener address, INTERNAL_HOST joined
// with INTERNAL_PORT.
//
// An empty INTERNAL_HOST listens on every interface, which suits a local run.
// A deployment sets it to the container's address on the private service
// network, so the private listener is not reachable from the reverse proxy's
// network.
func (cfg Config) InternalAddress() string {
	return net.JoinHostPort(cfg.InternalHost, cfg.InternalPort)
}

// validListenHost applies the shared INTERNAL_HOST rule: a host name or an IP
// address, without a port.
func validListenHost(host string) bool {
	reader := sharedconfig.NewReader(func(name string) (string, bool) {
		if name == "INTERNAL_HOST" {
			return host, true
		}
		return "", false
	})
	reader.InternalAddress(1)

	return reader.Err() == nil
}
