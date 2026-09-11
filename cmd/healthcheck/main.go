package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const defaultTimeout = 3 * time.Second

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	timeout := flags.Duration("timeout", defaultTimeout, "HTTP request timeout")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: healthcheck [-timeout duration] URL")
		return 1
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "healthcheck timeout must be positive")
		return 1
	}

	client := &http.Client{Timeout: *timeout}
	response, err := client.Get(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "healthcheck request failed: %v\n", err)
		return 1
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusBadRequest {
		fmt.Fprintf(stderr, "healthcheck returned %s\n", response.Status)
		return 1
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		fmt.Fprintf(stderr, "healthcheck response failed: %v\n", err)
		return 1
	}
	return 0
}
