//go:build windows

package statuslinebundle

import "embed"

//go:embed assets/*
var assets embed.FS

func Binary() ([]byte, error) { return assets.ReadFile("assets/submon.exe") }
