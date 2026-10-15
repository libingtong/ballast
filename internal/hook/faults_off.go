//go:build !ballast_fault

package hook

func faultsFromEnv(func(string) (string, bool)) FaultInjector { return nil }
