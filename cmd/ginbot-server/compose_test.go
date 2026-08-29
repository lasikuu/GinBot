package main

import (
	"os"
	"regexp"
	"testing"
	"time"
)

// Regex over YAML, to avoid a dependency this binary has no other use for.

const composePath = "../../docker-compose.prod.yml"

func readCompose(t *testing.T) string {
	t.Helper()

	raw, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("reading %s: %v", composePath, err)
	}

	return string(raw)
}

// A SIGKILL mid-drain skips the deferred db.CloseDB and log.Sync.
func TestComposeStopGracePeriodOutlastsShutdown(t *testing.T) {
	compose := readCompose(t)

	match := regexp.MustCompile(`(?m)^\s*stop_grace_period:\s*(\S+)\s*$`).FindStringSubmatch(compose)
	if match == nil {
		t.Fatalf("no stop_grace_period found in %s; ginbot-server needs one, "+
			"or the runtime's 10s default cuts the drain short", composePath)
	}

	// Compose accepts "1m30s", so parse rather than match whole seconds.
	grace, err := time.ParseDuration(match[1])
	if err != nil {
		t.Fatalf("stop_grace_period %q is not a duration: %v", match[1], err)
	}

	worstCase := shutdownDrainDelay + shutdownTimeout
	if grace <= worstCase {
		t.Errorf("stop_grace_period = %s, want strictly greater than shutdownDrainDelay + shutdownTimeout (%s)",
			grace, worstCase)
	}
}

// The probe dials loopback from inside the container, so the bind must be a
// wildcard. See ADR 0032.
func TestComposeHealthcheckProbesAnAddressTheServerBinds(t *testing.T) {
	compose := readCompose(t)

	// Exec form only, so psql's CMD-SHELL pg_isready check does not match.
	probe := regexp.MustCompile(`(?m)^\s*test:\s*\[\s*"CMD"\s*,\s*"([^"]*ginbot-server)"\s*,\s*"([^"]+)"\s*\]`).
		FindStringSubmatch(compose)
	if probe == nil {
		t.Fatalf("no exec-form [\"CMD\", \".../ginbot-server\", ...] healthcheck found in %s; "+
			"the probe must be this binary, because no external HTTP client in the runtime image "+
			"can present the client certificate the server requires under GINBOT_GRPC_TLS=true",
			composePath)
	}

	if probe[2] != healthProbeArg {
		t.Errorf("healthcheck runs %q %q; want the probe argument %q",
			probe[1], probe[2], healthProbeArg)
	}

	// The override is last: Compose merges a service's environment after the anchor.
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
			bind, healthProbeHost)
	}

	// No port assertion: both read GINBOT_GRPC_PORT from the same environment.
}

// Trigger media is persistent state whose index lives in Postgres, so losing
// the blobs leaves `file` rows pointing at nothing. See ADR 0007.
func TestComposeStoragePathIsMountedOnANamedVolume(t *testing.T) {
	compose := readCompose(t)

	declared := regexp.MustCompile(`(?m)^\s*GINBOT_STORAGE_PATH:\s*"?([^"\s]+)"?\s*$`).FindStringSubmatch(compose)
	if declared == nil {
		t.Fatalf("no GINBOT_STORAGE_PATH found in %s; without it the server writes trigger media to "+
			"./storage in the container's writable layer, which dies with the container on every "+
			"image update while the file rows referencing it survive in Postgres", composePath)
	}
	path := declared[1]

	// Named volume only: a bind mount would be host-path-dependent, and the
	// short syntax puts the source before the colon either way.
	mount := regexp.MustCompile(`(?m)^\s*-\s*([A-Za-z0-9_][A-Za-z0-9_.-]*):` + regexp.QuoteMeta(path) + `(?::[a-z,]+)?\s*$`).
		FindStringSubmatch(compose)
	if mount == nil {
		t.Fatalf("GINBOT_STORAGE_PATH is %q but no named volume is mounted there in %s", path, composePath)
	}
	name := mount[1]

	// Top-level `volumes:` entries are the only ones at two-space indent.
	if !regexp.MustCompile(`(?m)^\s{2}` + regexp.QuoteMeta(name) + `:\s*$`).MatchString(compose) {
		t.Errorf("volume %q is mounted at %s but never declared in the top-level volumes block of %s",
			name, path, composePath)
	}
}

// The postgres image reads POSTGRES_PASSWORD and nothing else, but .env should
// spell the secret once, under the name internal/config and example.env use.
func TestComposeReadsTheDatabasePasswordUnderOneName(t *testing.T) {
	compose := readCompose(t)

	// Every ${...} interpolation naming a password, whatever key it feeds.
	sources := regexp.MustCompile(`\$\{([A-Za-z0-9_]*PASSWORD[A-Za-z0-9_]*)[:?\-}]`).FindAllStringSubmatch(compose, -1)
	if len(sources) == 0 {
		t.Fatalf("no interpolated password variable found in %s", composePath)
	}

	for _, source := range sources {
		if source[1] != "GINBOT_DB_PASSWORD" {
			t.Errorf("%s reads the database password from ${%s}; want ${GINBOT_DB_PASSWORD}, the name "+
				"internal/config reads and example.env documents. A second spelling means `cp example.env .env` "+
				"produces a stack that refuses to start.", composePath, source[1])
		}
	}
}

// auth.ClientTLSConfig sets no ServerName, so a missing SAN fails every
// handshake with nothing hinting at why.
func TestComposeServerCertSANCoversTheDialledHost(t *testing.T) {
	compose := readCompose(t)

	hosts := regexp.MustCompile(`(?m)^\s*GINBOT_GRPC_HOST:\s*"?([^"\s]+)"?\s*$`).FindAllStringSubmatch(compose, -1)
	if len(hosts) == 0 {
		t.Fatalf("no GINBOT_GRPC_HOST found in %s", composePath)
	}
	// The first is the anchor's: the address the clients dial.
	dialled := hosts[0][1]

	ext, err := os.ReadFile("../../cert/server-ext.conf")
	if err != nil {
		t.Fatalf("reading cert/server-ext.conf: %v", err)
	}

	// Scoped to the subjectAltName line, not the file's comments.
	sanLine := regexp.MustCompile(`(?m)^\s*subjectAltName\s*=\s*(.+)$`).FindStringSubmatch(string(ext))
	if sanLine == nil {
		t.Fatalf("no subjectAltName found in cert/server-ext.conf:\n%s", ext)
	}

	// Explicit terminator, not \b: "-" is not a word character.
	if !regexp.MustCompile(`DNS:` + regexp.QuoteMeta(dialled) + `(?:,|\s|$)`).MatchString(sanLine[1]) {
		t.Errorf("cert/server-ext.conf has no DNS:%s SAN, but the platform clients dial that host "+
			"(GINBOT_GRPC_HOST in %s); every handshake would fail hostname verification with "+
			"GINBOT_GRPC_TLS=true.\nserver-ext.conf:\n%s", dialled, composePath, ext)
	}
}
