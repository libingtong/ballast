//go:build ballast_fault

package hook

// EnvFault is the environment variable read by fault-enabled builds.
const EnvFault = "BALLAST_FAULT"

func faultsFromEnv(getenv func(string) (string, bool)) FaultInjector {
	v, ok := getenv(EnvFault)
	if !ok || v == "" {
		return nil
	}
	p, m, err := parseFaultSpec(v)
	if err != nil {
		return nil
	}
	return singleFault{point: p, mode: m}
}
