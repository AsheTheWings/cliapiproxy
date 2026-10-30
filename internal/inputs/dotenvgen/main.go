// Command dotenvgen writes the generated .env to standard output; it reads
// no inputs. scripts/generate-env.sh runs it.
package main

import (
	"fmt"
	"os"

	"github.com/AsheTheWings/cliapiproxy/internal/inputs"
)

func main() {
	schema, errLoad := inputs.Load()
	if errLoad != nil {
		fmt.Fprintln(os.Stderr, errLoad)
		os.Exit(1)
	}
	if _, errWrite := os.Stdout.Write(schema.RenderDotenv()); errWrite != nil {
		fmt.Fprintln(os.Stderr, errWrite)
		os.Exit(1)
	}
}
