package main

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// docker-compose.prod.yml and this binary hold a set of numbers and addresses
// that only work together, and nothing but prose connected them.
//
// That is not hypothetical caution. The healthcheck shipped in review probing
// http://localhost:50051, while the anchor set GINBOT_GRPC_HOST to the Compose
// service name — which cmd/ginbot-server passes straight to net.Listen, so the
// listener bound the container's eth0 address alone and loopback was never
// bound at all. The probe could never succeed, the server could never report
// healthy, and both platform clients gate on `condition: service_healthy`, so
// the entire stack refused to start. Everything compiled, every Go test
// passed, and `docker compose config` was happy.
//
// So these tests parse the compose file. They are deliberately crude — regular
// expressions over YAML, not a schema-aware parse — because the alternative is
// a YAML dependency in a binary that has no other use for one, and the three
// relationships worth pinning are all single lines.

const composePath = "../../docker-compose.prod.yml"

func readCompose(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("reading %s: %v", composePath, err)
	}

	return string(raw)
}

// TestComposeStopGracePeriodOutlastsShutdown pins the relationship the
// stop_grace_period comment states: the container runtime must not SIGKILL
// this process while it is still draining, or the deferred db.CloseDB and
// log.Sync never run and the pool is torn down under in-flight work.
func TestComposeStopGracePeriodOutlastsShutdown(t *testing.T) {
	compose := readCompose(t)

	match := regexp.MustCompile(`(?m)^\s*stop_grace_period:\s*(\d+)s\s*$`).FindStringSubmatch(compose)
	if match == nil {
		t.Fatalf("no stop_grace_period found in %s; ginbot-server needs one, "+
			"or the runtime's 10s default cuts the drain short", composePath)
	}

	seconds, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("stop_grace_period %q is not a whole number of seconds: %v", match[1], err)
	}

	grace := time.Duration(seconds) * time.Second
	worstCase := shutdownDrainDelay + shutdownTimeout
	if grace <= worstCase {
		t.Errorf("stop_grace_period = %s, want strictly greater than shutdownDrainDelay + shutdownTimeout (%s)",
			grace, worstCase)
	}
}

// TestComposeHealthcheckProbesAnAddressTheServerBinds is the guard for the bug
// described at the top of this file.
//
// The rule it enforces: whatever host the healthcheck connects to, the
// ginbot-server service must set GINBOT_GRPC_HOST to a wildcard, because the
// probe runs inside the container and the server binds exactly the one address
// that variable names. Binding a specific name or address and probing a
// different one is the failure that shipped.
func TestComposeHealthcheckProbesAnAddressTheServerBinds(t *testing.T) {
	compose := readCompose(t)

	probe := regexp.MustCompile(`wget[^"]*http://([^:/"]+):(\d+)/healthz`).FindStringSubmatch(compose)
	if probe == nil {
		t.Fatalf("no /healthz wget healthcheck found in %s", composePath)
	}

	// Every GINBOT_GRPC_HOST in the file: the anchor's (what the clients dial)
	// and ginbot-server's own override (what the server binds). The override
	// is the last one, because Compose merge order puts a service's own
	// environment after the anchor it spreads.
	hosts := regexp.MustCompile(`(?m)^\s*GINBOT_GRPC_HOST:\s*"?([^"\s]+)"?\s*$`).FindAllStringSubmatch(compose, -1)
	if len(hosts) < 2 {
		t.Fatalf("expected GINBOT_GRPC_HOST twice in %s (the anchor, and ginbot-server's own override); found %d",
			composePath, len(hosts))
	}

	bind := hosts[len(hosts)-1][1]
	if bind != "0.0.0.0" && bind != "::" {
		t.Errorf("ginbot-server binds GINBOT_GRPC_HOST=%q but the healthcheck probes %q; "+
			"the server binds exactly the address that variable names, so a non-wildcard bind "+
			"leaves the probe's address unbound and the container never becomes healthy",
			bind, probe[1])
	}

	if probe[2] != "50051" {
		t.Errorf("healthcheck probes port %s; GINBOT_GRPC_PORT in the anchor is 50051", probe[2])
	}
}

// TestComposeServerCertSANCoversTheDialledHost pins the other cross-artifact
// relationship the compose comments claim: enabling mutual TLS must be
// `cd cert && ./generator.sh` plus GINBOT_GRPC_TLS=true, with no further edit.
//
// That only holds while cert/server-ext.conf carries a SAN matching the host
// the clients dial. auth.ClientTLSConfig sets neither ServerName nor
// InsecureSkipVerify — on purpose, and there is a test pinning it — so a
// missing SAN makes every client handshake fail hostname verification, with
// nothing in either file hinting at why.
func TestComposeServerCertSANCoversTheDialledHost(t *testing.T) {
	compose := readCompose(t)

	hosts := regexp.MustCompile(`(?m)^\s*GINBOT_GRPC_HOST:\s*"?([^"\s]+)"?\s*$`).FindAllStringSubmatch(compose, -1)
	if len(hosts) == 0 {
		t.Fatalf("no GINBOT_GRPC_HOST found in %s", composePath)
	}
	// The FIRST one is the anchor's: the address the clients dial.
	dialled := hosts[0][1]

	ext, err := os.ReadFile("../../cert/server-ext.conf")
	if err != nil {
		t.Fatalf("reading cert/server-ext.conf: %v", err)
	}

	if !regexp.MustCompile(`DNS:` + regexp.QuoteMeta(dialled) + `\b`).Match(ext) {
		t.Errorf("cert/server-ext.conf has no DNS:%s SAN, but the platform clients dial that host "+
			"(GINBOT_GRPC_HOST in %s); every handshake would fail hostname verification with "+
			"GINBOT_GRPC_TLS=true.\nserver-ext.conf:\n%s", dialled, composePath, ext)
	}
}
