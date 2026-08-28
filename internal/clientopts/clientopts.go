// Package clientopts translates configuration into the transport options the
// platform clients dial ginbot-server with.
//
// It is a leaf on purpose, and the reason is a test-visibility one rather than
// an aesthetic one. This is the only code that needs both internal/config and
// pkg/grpc/client, and putting it in internal/config instead would make
// internal/config import pkg/grpc/client — which sounds harmless, but
// pkg/grpc/server -> pkg/db -> internal/config, so pkg/grpc/client's own
// white-box tests could then no longer import pkg/grpc/server. That matters:
// the reverse action stream's reconnect loop is only worth testing against the
// REAL ReverseServer, cap and clearance interceptor included, rather than
// against a stub that reproduces whatever the client already believes.
//
// pkg/discord and pkg/matrix import this; nothing else should need to.
package clientopts

import (
	"github.com/lasikuu/GinBot/internal/auth"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
)

// messageBytes bounds a single Connect message this process sends or
// receives. It matches the server's own baselineMessageBytes
// (cmd/ginbot-server/main.go): no message on this boundary carries a whole
// file any more, the largest is a 1 MiB GetFileChunk, so one value covers
// every service including TriggerService. This is self-protection against a
// misbehaving or compromised server, not a DoS boundary — a platform client
// only ever talks to the ginbot-server it was configured to dial, so there is
// no untrusted peer here the way there is on the server's own side of this
// cap.
const messageBytes = 4 << 20

// Dial builds the client.Options ginbot-discord and ginbot-matrix dial the
// Connect boundary with.
//
// This is the ONE place the config -> client.Options translation happens,
// shared by pkg/discord and pkg/matrix rather than copy-pasted into both.
//
// auth.ClientTLSConfig is loaded lazily, here, only when TLS is actually on:
// config.GRPCServerOptions itself intentionally holds no built transport (see
// its doc comment) so that config.SetEnv never fails a plaintext deployment
// for want of certificates it will not use.
func Dial() (client.Options, error) {
	opts := client.Options{
		BaseURL:      config.Options.GRPC.ClientBaseURL(),
		MaxRecvBytes: messageBytes,
		MaxSendBytes: messageBytes,
	}

	if !config.Options.GRPC.TLS {
		return opts, nil
	}

	tlsConfig, err := auth.ClientTLSConfig(config.Options.GRPC.CertsPath)
	if err != nil {
		return client.Options{}, err
	}
	opts.TLS = tlsConfig

	return opts, nil
}
