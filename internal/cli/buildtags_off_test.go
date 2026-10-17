//go:build !ballast_test

package cli_test

// childTagNames: e2e 二进制只带 ballast_fault,不叠加 ballast_test(此文件为无 ballast_test 标签的构建,子进程不带该标签)。
func childTagNames() []string { return nil }
