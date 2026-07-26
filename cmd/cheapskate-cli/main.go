// cheapskate の設定用 CLI であり、実装は internal/ui/cli にある
package main

import (
	"os"

	"cheapskate/internal/ui/cli"
)

func main() { cli.Main(os.Args[1:]) }
