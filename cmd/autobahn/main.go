package main

import "flag"

func main() {
	var targetURL string
	var includeCasesArg string
	flag.StringVar(&targetURL, "target", "http://localhost:8080", "Target URL")
	flag.StringVar(&includeCasesArg, "include", "", "Test cases to include, separated by commas (e.g. '1.*,2.*,3.1.1')")
	flag.Parse()

}
