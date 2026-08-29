// Package interceptor_test, not interceptor: this file needs
// pkg/grpc/service.RegisteredServiceNames to restrict coverage to what
// cmd/ginbot-server actually mounts, and pkg/grpc/service imports
// pkg/grpc/server, which imports pkg/grpc/interceptor for its production
// code. Importing pkg/grpc/service from an INTERNAL interceptor test file
// (package interceptor) is a genuine import cycle — internal test files are
// compiled as part of the package itself — but importing it from an external
// test package that merely depends on interceptor is not: Go compiles
// package_test as a separate unit that is allowed to depend on anything that
// itself depends on package. Every other test file in this directory stays
// package interceptor; this is the one exception, and the reason is load-
// bearing rather than stylistic.
package interceptor_test

import (
	"slices"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/gen/ginbot/v1/ginbotv1connect"
	"github.com/lasikuu/GinBot/pkg/grpc/interceptor"
	"github.com/lasikuu/GinBot/pkg/grpc/service"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// This file replaces a hand-maintained list of grpc.ServiceDesc values (which
// no longer exist post-port) with a walk over protobuf reflection. The
// property under test — "every reachable, non-public procedure is guarded" —
// is now enforced by the SCHEMA rather than by a Go slice someone has to
// remember to extend, which is the whole point: a newly added RPC absent from
// interceptor.DefaultRequirements() fails
// TestRequirementsCoverEveryMountedProcedure without anyone editing this file.
//
// wirePackage restricts the walk to this repository's own proto package, the
// same way pkg/grpc/server/wire_test.go does, so google.protobuf and
// buf.validate descriptors registered in the same global registry are never
// mistaken for ours.
const wirePackage protoreflect.FullName = "ginbot.v1"

// procedureOf reproduces the generated ginbotv1connect.*Procedure shape from
// reflection: "/" + service full name + "/" + method name. It is asserted to
// be byte-identical to the generated constants by every test below that
// compares its output against one.
func procedureOf(service protoreflect.ServiceDescriptor, method protoreflect.MethodDescriptor) string {
	return "/" + string(service.FullName()) + "/" + string(method.Name())
}

// mountedServiceNames indexes service.RegisteredServiceNames — the services
// cmd/ginbot-server actually mounts on its ServeMux — for O(1) lookup.
// Restricting coverage to these is deliberate: a service nothing serves
// cannot be reached, and so cannot be left unguarded. DiscordService is the
// one service in the schema NOT in this set.
func mountedServiceNames() map[string]bool {
	mounted := service.RegisteredServiceNames()

	names := make(map[string]bool, len(mounted))
	for _, name := range mounted {
		names[name] = true
	}
	return names
}

// rangeMountedMethods calls fn once per method of every ginbot.v1 service that
// cmd/ginbot-server mounts, with the procedure string a real client would send.
//
// It fatals if it finds nothing: protoregistry.GlobalFiles is populated by the
// generated package's own init, reached here only because this test imports
// packages that import it transitively. An empty registry would otherwise let
// every assertion below pass vacuously.
func rangeMountedMethods(t *testing.T, fn func(procedure string, method protoreflect.MethodDescriptor)) {
	t.Helper()

	mounted := mountedServiceNames()
	found := false

	protoregistry.GlobalFiles.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if file.Package() != wirePackage {
			return true
		}

		services := file.Services()
		for i := range services.Len() {
			svc := services.Get(i)
			if !mounted[string(svc.FullName())] {
				continue
			}
			found = true

			methods := svc.Methods()
			for j := range methods.Len() {
				method := methods.Get(j)
				fn(procedureOf(svc, method), method)
			}
		}
		return true
	})

	if !found {
		t.Fatalf("no mounted %s service was found in the registry; the walk below would pass vacuously", wirePackage)
	}
}

// allKnownProcedures is the wider set rangeMountedMethods deliberately does
// not use: every procedure declared in the schema, mounted or not. It exists
// so TestEveryRequirementKeyIsARealProcedure can accept
// DiscordService/SetDiscordActivityType — declared in DefaultRequirements at
// administrator, but DiscordService is not in service.RegisteredServiceNames
// yet — without also being satisfied by a typo, which is what the mounted-only
// set would let through as "not mounted, so who cares".
func allKnownProcedures(t *testing.T) map[string]bool {
	t.Helper()

	known := make(map[string]bool)
	found := false

	protoregistry.GlobalFiles.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		if file.Package() != wirePackage {
			return true
		}

		services := file.Services()
		for i := range services.Len() {
			svc := services.Get(i)
			found = true
			methods := svc.Methods()
			for j := range methods.Len() {
				known[procedureOf(svc, methods.Get(j))] = true
			}
		}
		return true
	})

	if !found {
		t.Fatalf("no %s service was found in the registry; the walk below would pass vacuously", wirePackage)
	}

	return known
}

// productionPublicMethods are the procedures deliberately absent from
// interceptor.DefaultRequirements(), per its own doc comment: the caller has no account
// yet (Register), or the method exposes no user data (HealthCheck, Ping,
// GetRandomNumber).
func productionPublicMethods() map[string]bool {
	return map[string]bool{
		ginbotv1connect.UserServiceRegisterProcedure:                 true,
		ginbotv1connect.UtilityServiceHealthCheckProcedure:           true,
		ginbotv1connect.UtilityServicePingProcedure:                  true,
		ginbotv1connect.EntertainmentServiceGetRandomNumberProcedure: true,
	}
}

// missingFromRequirements reports every MOUNTED, non-public procedure absent
// from reqs. It is a plain function rather than a *testing.T assertion so
// TestRequirementsCoverageCatchesAMissingProcedure can drive it with a
// deliberately incomplete map and inspect the result — the negative case that
// proves TestRequirementsCoverEveryMountedProcedure actually fails when a key
// is dropped, rather than merely looking like it would.
func missingFromRequirements(t *testing.T, reqs interceptor.Requirements, public map[string]bool) []string {
	t.Helper()

	var missing []string
	rangeMountedMethods(t, func(procedure string, _ protoreflect.MethodDescriptor) {
		if public[procedure] {
			return
		}
		if _, declared := reqs[procedure]; !declared {
			missing = append(missing, procedure)
		}
	})
	return missing
}

// TestRequirementsCoverEveryMountedProcedure is the durable replacement for
// the hand-maintained list. Add a tenth handler to TriggerService, or a new
// service to service.RegisteredServiceNames, and forget to declare it here:
// this fails, without anyone editing a Go slice to notice the addition.
func TestRequirementsCoverEveryMountedProcedure(t *testing.T) {
	missing := missingFromRequirements(t, interceptor.DefaultRequirements(), productionPublicMethods())
	for _, procedure := range missing {
		t.Errorf("%s is mounted and not declared public, but is absent from interceptor.DefaultRequirements(); "+
			"it is reachable and currently unguarded", procedure)
	}
}

// TestRequirementsCoverageCatchesAMissingProcedure is the negative case: it
// proves the coverage test above is not vacuous by deleting one declared,
// mounted, non-public key and checking that missingFromRequirements actually
// reports it. Without this, a bug in rangeMountedMethods or in the public set
// could make TestRequirementsCoverEveryMountedProcedure pass regardless of
// what interceptor.DefaultRequirements() contains.
func TestRequirementsCoverageCatchesAMissingProcedure(t *testing.T) {
	incomplete := interceptor.DefaultRequirements()

	const victim = ginbotv1connect.TriggerServiceTryTriggerProcedure
	if _, ok := incomplete[victim]; !ok {
		t.Fatalf("fixture assumption broken: %s is expected to already be declared in interceptor.DefaultRequirements()", victim)
	}
	delete(incomplete, victim)

	missing := missingFromRequirements(t, incomplete, productionPublicMethods())

	found := slices.Contains(missing, victim)
	if !found {
		t.Fatalf("removing %s from the map was not reported by missingFromRequirements (reported: %v); "+
			"the coverage test would not catch a dropped procedure", victim, missing)
	}
}

// Public methods are the ones a caller must be able to reach before they have
// an account. Register especially: guarding it makes registration impossible.
func TestPublicMethodsAreAbsentFromRequirements(t *testing.T) {
	reqs := interceptor.DefaultRequirements()

	for method := range productionPublicMethods() {
		t.Run(method, func(t *testing.T) {
			// Absence is what makes a method public. A present entry set to
			// CLEARANCE_UNSPECIFIED would also let everyone through today,
			// but it would resolve the caller first and so fail for anyone
			// who has not registered.
			if clearance, declared := reqs[method]; declared {
				t.Errorf("%s is declared as %v, want it absent so no caller is resolved", method, clearance)
			}
		})
	}
}

func TestInstanceMutationRequiresAdministrator(t *testing.T) {
	reqs := interceptor.DefaultRequirements()
	mutating := []string{
		ginbotv1connect.InstanceServiceCreateInstanceProcedure,
		ginbotv1connect.InstanceServiceUpdateInstanceProcedure,
		ginbotv1connect.InstanceServiceDeleteInstanceProcedure,
	}

	for _, method := range mutating {
		t.Run(method, func(t *testing.T) {
			clearance, declared := reqs[method]
			if !declared {
				t.Fatalf("%s is public; it must require CLEARANCE_ADMINISTRATOR", method)
			}
			if clearance != pb.Clearance_CLEARANCE_ADMINISTRATOR {
				t.Errorf("%s requires %v, want %v", method, clearance, pb.Clearance_CLEARANCE_ADMINISTRATOR)
			}
		})
	}
}

// Everything mounted that is not deliberately public must need an account. A
// method that is simply forgotten becomes public by default, which is the
// failure mode this test exists to catch.
func TestEveryNonPublicMountedMethodRequiresAtLeastRegistered(t *testing.T) {
	reqs := interceptor.DefaultRequirements()
	public := productionPublicMethods()

	rangeMountedMethods(t, func(procedure string, _ protoreflect.MethodDescriptor) {
		if public[procedure] {
			return
		}

		t.Run(procedure, func(t *testing.T) {
			clearance, declared := reqs[procedure]
			if !declared {
				t.Fatalf("%s is not declared and is therefore public", procedure)
			}
			if clearance < pb.Clearance_CLEARANCE_REGISTERED {
				t.Errorf("%s requires %v, want at least %v", procedure, clearance, pb.Clearance_CLEARANCE_REGISTERED)
			}
		})
	})
}

// CLEARANCE_UNSPECIFIED is 0, so every caller satisfies it. Declaring a method
// at that level pays the cost of resolving the caller and grants nothing.
func TestNoRequirementIsUnspecified(t *testing.T) {
	for method, clearance := range interceptor.DefaultRequirements() {
		if clearance == pb.Clearance_CLEARANCE_UNSPECIFIED {
			t.Errorf("%s is declared as CLEARANCE_UNSPECIFIED; leave it out of the map instead", method)
		}
	}
}

// A key with a typo silently makes its method public, and nothing else in the
// system would notice. Checked against EVERY known procedure, not just the
// mounted ones, so a declared-but-not-yet-mounted entry (DiscordService,
// below) is not mistaken for an orphan.
func TestEveryRequirementKeyIsARealProcedure(t *testing.T) {
	known := allKnownProcedures(t)

	for method := range interceptor.DefaultRequirements() {
		if !known[method] {
			t.Errorf("%q is not a generated ginbot.v1 procedure", method)
		}
	}
}

// OpenClientActionStream is a stream, not a unary call, but stage 3 puts it
// through exactly the same ClearanceInterceptor.WrapStreamingHandler and the
// same map. Before this it had no requirements entry at all — the map only
// drove the unary interceptor — which is the hole ADR-0012 records and the
// whole point of this stage.
func TestOpenClientActionStreamIsRegisteredInTheMap(t *testing.T) {
	clearance, declared := interceptor.DefaultRequirements()[ginbotv1connect.ReverseServiceOpenClientActionStreamProcedure]
	if !declared {
		t.Fatal("OpenClientActionStream is absent from interceptor.DefaultRequirements(); an unauthorised caller could open it")
	}
	if clearance != pb.Clearance_CLEARANCE_REGISTERED {
		t.Errorf("OpenClientActionStream requires %v, want %v", clearance, pb.Clearance_CLEARANCE_REGISTERED)
	}
}

// TestDiscordServiceIsGuardedButNotMounted pins the one deliberate exception
// to "coverage is restricted to mounted services": DiscordService has a
// declared floor in interceptor.DefaultRequirements() even though its server
// implementation does not exist yet, so the floor is already in place before
// the method is reachable at all. If this ever starts failing because
// DiscordService IS mounted, TestEveryNonPublicMountedMethodRequiresAtLeastRegistered
// already covers it from that point on and this pin can be deleted.
func TestDiscordServiceIsGuardedButNotMounted(t *testing.T) {
	clearance, declared := interceptor.DefaultRequirements()[ginbotv1connect.DiscordServiceSetDiscordActivityTypeProcedure]
	if !declared {
		t.Fatal("DiscordService/SetDiscordActivityType is absent from interceptor.DefaultRequirements(); " +
			"it must stay declared even though the service is not mounted yet")
	}
	if clearance != pb.Clearance_CLEARANCE_ADMINISTRATOR {
		t.Errorf("DiscordService/SetDiscordActivityType requires %v, want %v",
			clearance, pb.Clearance_CLEARANCE_ADMINISTRATOR)
	}

	if mountedServiceNames()[ginbotv1connect.DiscordServiceName] {
		t.Error("DiscordService is now mounted; TestEveryNonPublicMountedMethodRequiresAtLeastRegistered " +
			"already asserts its floor from here on, so this pin is redundant and should be removed")
	}
}
