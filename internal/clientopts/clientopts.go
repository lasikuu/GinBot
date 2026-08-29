// Package clientopts translates configuration into the transport options the
// platform clients dial with. A separate leaf so internal/config need not
// import pkg/grpc/client, which would cycle back through pkg/db.
package clientopts

import (
	"github.com/lasikuu/GinBot/internal/auth"
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/lasikuu/GinBot/pkg/grpc/client"
)

// messageBytes matches the server's baselineMessageBytes.
const messageBytes = 4 << 20

// TLS material is loaded here, not in config.SetEnv, so a plaintext
// deployment never fails for want of certificates it will not use.
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
