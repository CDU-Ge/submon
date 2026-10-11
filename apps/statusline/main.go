// The standalone one-shot renderer shares its behavior with submon statusline.
package main

import (
	"github.com/ge/subscription-monitor/internal/statusline"
	"os"
)

func main() { os.Exit(statusline.Execute(os.Args[1:])) }
