package config

import "os"

// env 抽一层，方便单测里用 t.Setenv 覆盖（避免直接依赖 os.Getenv 的隐式行为）。
func env(key string) string { return os.Getenv(key) }
