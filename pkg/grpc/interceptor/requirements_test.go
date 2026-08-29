// External test package: importing pkg/grpc/service from package interceptor
// would be an import cycle.
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

// wirePackage restricts the reflection walk to our own protos.
const wirePackage protoreflect.FullName = "ginbot.v1"

// procedureOf reproduces the generated ginbotv1connect.*Procedure shape.
func procedureOf(service protoreflect.ServiceDescriptor, method protoreflect.MethodDescriptor) string {
	return "/" + string(service.FullName()) + "/" + string(method.Name())
}

// mountedServiceNames indexes the services cmd/ginbot-server mounts.
func mountedServiceNames() map[string]bool {
	mounted := service.RegisteredServiceNames()

	names := make(map[string]bool, len(mounted))
	for _, name := range mounted {
		names[name] = true
	}
	return names
}

// rangeMountedMethods calls fn once per method of every mounted ginbot.v1
// service, fataling on an empty registry.
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

// allKnownProcedures is every procedure in the schema, mounted or not.
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

// productionPublicMethods are deliberately absent from DefaultRequirements().
func productionPublicMethods() map[string]bool {
	return map[string]bool{
		ginbotv1connect.UserServiceRegisterProcedure:                 true,
		ginbotv1connect.UtilityServiceHealthCheckProcedure:           true,
		ginbotv1connect.UtilityServicePingProcedure:                  true,
		ginbotv1connect.EntertainmentServiceGetRandomNumberProcedure: true,
	}
}

// missingFromRequirements reports every mounted, non-public procedure absent from reqs.
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

func TestRequirementsCoverEveryMountedProcedure(t *testing.T) {
	missing := missingFromRequirements(t, interceptor.DefaultRequirements(), productionPublicMethods())
	for _, procedure := range missing {
		t.Errorf("%s is mounted and not declared public, but is absent from interceptor.DefaultRequirements(); "+
			"it is reachable and currently unguarded", procedure)
	}
}

// Proves the coverage test above is not vacuous.
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

func TestPublicMethodsAreAbsentFromRequirements(t *testing.T) {
	reqs := interceptor.DefaultRequirements()

	for method := range productionPublicMethods() {
		t.Run(method, func(t *testing.T) {
			// Absence, not CLEARANCE_UNSPECIFIED, makes a method public.
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

// A forgotten method becomes public by default; this catches it.
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

// CLEARANCE_UNSPECIFIED is 0, so every caller satisfies it.
func TestNoRequirementIsUnspecified(t *testing.T) {
	for method, clearance := range interceptor.DefaultRequirements() {
		if clearance == pb.Clearance_CLEARANCE_UNSPECIFIED {
			t.Errorf("%s is declared as CLEARANCE_UNSPECIFIED; leave it out of the map instead", method)
		}
	}
}

// A key with a typo silently makes its method public.
func TestEveryRequirementKeyIsARealProcedure(t *testing.T) {
	known := allKnownProcedures(t)

	for method := range interceptor.DefaultRequirements() {
		if !known[method] {
			t.Errorf("%q is not a generated ginbot.v1 procedure", method)
		}
	}
}

// A stream goes through the same map as unary calls. See ADR-0012.
func TestOpenClientActionStreamIsRegisteredInTheMap(t *testing.T) {
	clearance, declared := interceptor.DefaultRequirements()[ginbotv1connect.ReverseServiceOpenClientActionStreamProcedure]
	if !declared {
		t.Fatal("OpenClientActionStream is absent from interceptor.DefaultRequirements(); an unauthorised caller could open it")
	}
	if clearance != pb.Clearance_CLEARANCE_REGISTERED {
		t.Errorf("OpenClientActionStream requires %v, want %v", clearance, pb.Clearance_CLEARANCE_REGISTERED)
	}
}

// DiscordService has a floor declared before its implementation is mounted.
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
