// Command openapi writes Mapine's generated OpenAPI document to stdout.
package main

import (
	"fmt"
	"os"

	"github.com/mirzawho/mapine-services/internal/docs"
)

func main() {
	document, err := docs.OpenAPIJSON()
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate OpenAPI document:", err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(document); err != nil {
		fmt.Fprintln(os.Stderr, "write OpenAPI document:", err)
		os.Exit(1)
	}
}
