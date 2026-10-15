// Command noop 是 M0c 的对照基线:Go 运行时启动后立即退出,
// 用来分离"Go 进程创建 + 运行时初始化"与"hook 自身工作"的耗时。
package main

func main() {}
