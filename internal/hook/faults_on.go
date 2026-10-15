//go:build ballast_fault

package hook

// EnvFault is the environment variable read by fault-enabled builds.
const EnvFault = "BALLAST_FAULT"

func faultsFromEnv(getenv func(string) (string, bool)) FaultInjector {
	panic("unimplemented: hook.faultsFromEnv (ballast_fault build)")
}
