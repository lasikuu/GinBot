package interceptor

import (
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
	"google.golang.org/grpc"
)

// productionRequirements is the map the server actually installs.
//
// The specified interface names Requirements, CallerResolver,
// NewClearanceUnaryInterceptor and CallerFromContext but not the map itself, so
// every assertion below reaches it through this single alias. If the
// implementation named it differently, exactly one line changes here.
var productionRequirements = DefaultRequirements()

// servedDescriptors are the services cmd/ginbot-server actually registers.
// Requirements coverage is asserted only over these: TriggerService and
// DiscordService have generated stubs but no server implementation, so whether
// they are declared yet is not something this test should dictate.
func servedDescriptors() []grpc.ServiceDesc {
	return []grpc.ServiceDesc{
		pb.UserService_ServiceDesc,
		pb.UtilityService_ServiceDesc,
		pb.InstanceService_ServiceDesc,
		pb.ReminderService_ServiceDesc,
		pb.AnalyticsService_ServiceDesc,
		pb.EntertainmentService_ServiceDesc,
		pb.ReverseService_ServiceDesc,
	}
}

// knownDescriptors are every generated service, used to catch typos in map keys
// without also demanding that the unwired services be declared.
func knownDescriptors() []grpc.ServiceDesc {
	return append(servedDescriptors(),
		pb.TriggerService_ServiceDesc,
		pb.DiscordService_ServiceDesc,
	)
}

// unaryMethods lists full method names taken from the generated descriptors, so
// a newly added RPC turns up here on its own. Streams are excluded: a unary
// interceptor never sees them, and ReverseService.OpenClientActionStream is the
// only stream in the schema.
func unaryMethods(descriptors []grpc.ServiceDesc) []string {
	var methods []string
	for _, descriptor := range descriptors {
		for _, method := range descriptor.Methods {
			methods = append(methods, "/"+descriptor.ServiceName+"/"+method.MethodName)
		}
	}

	return methods
}

// Public methods are the ones a caller must be able to reach before they have
// an account. Register especially: guarding it makes registration impossible.
func TestPublicMethodsAreAbsentFromRequirements(t *testing.T) {
	public := []string{
		pb.UserService_Register_FullMethodName,
		pb.UtilityService_HealthCheck_FullMethodName,
		pb.UtilityService_Ping_FullMethodName,
		pb.EntertainmentService_GetRandomNumber_FullMethodName,
	}

	for _, method := range public {
		t.Run(method, func(t *testing.T) {
			// Absence is what makes a method public. A present entry set to
			// CLEARANCE_UNSPECIFIED would also let everyone through today, but
			// it would resolve the caller first and so fail for anyone who has
			// not registered.
			if clearance, declared := productionRequirements[method]; declared {
				t.Errorf("%s is declared as %v, want it absent so no caller is resolved", method, clearance)
			}
		})
	}
}

func TestInstanceMutationRequiresAdministrator(t *testing.T) {
	mutating := []string{
		pb.InstanceService_CreateInstance_FullMethodName,
		pb.InstanceService_UpdateInstance_FullMethodName,
		pb.InstanceService_DeleteInstance_FullMethodName,
	}

	for _, method := range mutating {
		t.Run(method, func(t *testing.T) {
			clearance, declared := productionRequirements[method]
			if !declared {
				t.Fatalf("%s is public; it must require CLEARANCE_ADMINISTRATOR", method)
			}
			if clearance != pb.Clearance_CLEARANCE_ADMINISTRATOR {
				t.Errorf("%s requires %v, want %v", method, clearance, pb.Clearance_CLEARANCE_ADMINISTRATOR)
			}
		})
	}
}

// Everything that is not deliberately public must need an account. A method
// that is simply forgotten becomes public by default, which is the failure mode
// this test exists to catch.
func TestEveryNonPublicMethodRequiresAtLeastRegistered(t *testing.T) {
	public := map[string]bool{
		pb.UserService_Register_FullMethodName:                 true,
		pb.UtilityService_HealthCheck_FullMethodName:           true,
		pb.UtilityService_Ping_FullMethodName:                  true,
		pb.EntertainmentService_GetRandomNumber_FullMethodName: true,
	}

	for _, method := range unaryMethods(servedDescriptors()) {
		if public[method] {
			continue
		}

		t.Run(method, func(t *testing.T) {
			clearance, declared := productionRequirements[method]
			if !declared {
				t.Fatalf("%s is not declared and is therefore public", method)
			}
			if clearance < pb.Clearance_CLEARANCE_REGISTERED {
				t.Errorf("%s requires %v, want at least %v", method, clearance, pb.Clearance_CLEARANCE_REGISTERED)
			}
		})
	}
}

// CLEARANCE_UNSPECIFIED is 0, so every caller satisfies it. Declaring a method
// at that level pays the cost of resolving the caller and grants nothing.
func TestNoRequirementIsUnspecified(t *testing.T) {
	for method, clearance := range productionRequirements {
		if clearance == pb.Clearance_CLEARANCE_UNSPECIFIED {
			t.Errorf("%s is declared as CLEARANCE_UNSPECIFIED; leave it out of the map instead", method)
		}
	}
}

// A key with a typo silently makes its method public, and nothing else in the
// system would notice.
func TestEveryRequirementKeyIsARealMethod(t *testing.T) {
	known := make(map[string]bool)
	for _, method := range unaryMethods(knownDescriptors()) {
		known[method] = true
	}

	for method := range productionRequirements {
		if !known[method] {
			t.Errorf("%q is not a generated unary method name", method)
		}
	}
}
