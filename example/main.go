// Example CLI: prints dominant colors of an image as JSON.
// Usage: go run ./example <path-to-image>
package main

import (
	"fmt"
	"os"

	"github.com/rigter/hue"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: main <image-path>")
		os.Exit(1)
	}

	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Println("error opening file:", err)
		os.Exit(1)
	}
	defer f.Close()

	jsonBytes, err := hue.ExtractJSON(f, hue.Options{
		NumColors:       5,
		MaxSampleDim:    100,
		IgnoreNearWhite: true,
	})
	if err != nil {
		fmt.Println("error extracting colors:", err)
		os.Exit(1)
	}

	fmt.Println(string(jsonBytes))
}
